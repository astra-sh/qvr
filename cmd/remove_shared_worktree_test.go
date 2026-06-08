package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
)

// TestRemove_KeepsWorktreeSharedWithAnotherProject is the #232 data-loss guard:
// two projects that install the same skill@sha share one global SHA-keyed
// worktree, so `qvr remove` in one must drop only that project's symlink + lock
// entry and KEEP the shared worktree another live project still references. Only
// when the last referencing project removes it is the worktree reclaimed.
func TestRemove_KeepsWorktreeSharedWithAnotherProject(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	resetPrinter(t)

	gc := git.NewGoGitClient()
	mgr := newRegistryManager(gc)
	inst := skill.NewInstaller(mgr, git.NewGoGitWorktree(), gc)

	remote := seedTaggedPullRemote(t, "benchmark", "v1.0.0")
	if _, err := mgr.Add(t.Context(), "acme", remote); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	install := func(projectRoot string) {
		if _, err := inst.Install(skill.InstallRequest{
			Skill: "benchmark@main", Targets: []string{"claude"}, ProjectRoot: projectRoot,
		}); err != nil {
			t.Fatalf("install into %s: %v", projectRoot, err)
		}
		registry.TouchProject(model.DefaultLockPath(projectRoot, config.Dir(), false))
	}

	projA := t.TempDir()
	projB := t.TempDir()
	install(projA)
	install(projB)

	// Both projects resolve to the SAME global worktree.
	lockB, _ := model.ReadLockFile(model.DefaultLockPath(projB, config.Dir(), false))
	entryB, _ := lockB.Get("benchmark")
	worktree := registry.WorktreePathForEntry(entryB)
	if worktree == "" {
		t.Fatal("could not derive shared worktree path")
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("precondition: shared worktree should exist: %v", err)
	}

	// Remove from A — must NOT delete the worktree B still shares.
	if err := inst.Remove("benchmark", skill.InstallRequest{ProjectRoot: projA}); err != nil {
		t.Fatalf("remove from A: %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Errorf("DATA LOSS: shared worktree deleted while project B still references it (err=%v)", err)
	}
	// A's symlink is gone; B's still resolves to live content.
	if _, err := os.Lstat(filepath.Join(projA, ".claude", "skills", "benchmark")); !os.IsNotExist(err) {
		t.Errorf("project A symlink should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projB, ".claude", "skills", "benchmark", "SKILL.md")); err != nil {
		t.Errorf("project B install should still resolve to live content: %v", err)
	}

	// Remove from B — now the last reference is gone, so the worktree is reclaimed.
	if err := inst.Remove("benchmark", skill.InstallRequest{ProjectRoot: projB}); err != nil {
		t.Fatalf("remove from B: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("worktree should be reclaimed once the last project removes it (stat err=%v)", err)
	}
}
