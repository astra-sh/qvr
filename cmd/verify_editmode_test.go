package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
)

// setupEditedCreatedSkill scaffolds a created (edit-mode, sourceless) skill in
// a fresh project and mutates its SKILL.md so the sealed subtree hash drifts.
// Returns the project root.
func setupEditedCreatedSkill(t *testing.T) string {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project := t.TempDir()
	t.Chdir(project)
	resetPrinter(t)

	t.Cleanup(func() {
		createStandalone = false
		createType = "simple"
		createTarget = "claude"
		createGlobal = false
	})
	createStandalone = false
	createType = "simple"
	createTarget = "claude"
	if err := runCreateProjectScoped("my-skill"); err != nil {
		t.Fatalf("create: %v", err)
	}

	md := filepath.Join(project, ".claude", "skills", "my-skill", "SKILL.md")
	f, err := os.OpenFile(md, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open SKILL.md: %v", err)
	}
	if _, err := f.WriteString("\nlocal tweak\n"); err != nil {
		t.Fatalf("append SKILL.md: %v", err)
	}
	_ = f.Close()
	return project
}

// TestLockVerify_EditModeDrift_HintsRepairNotRemoveAdd covers the unfiled
// e2e-round observation: editing a created skill in place makes `qvr lock
// verify` report drift (still fatal — drift is drift in CI), but the remedy
// must be `lock verify --repair`, never the `remove --force && add` hint,
// which cannot work for a sourceless local skill.
func TestLockVerify_EditModeDrift_HintsRepairNotRemoveAdd(t *testing.T) {
	setupEditedCreatedSkill(t)
	t.Cleanup(func() {
		lockVerifyRepair = false
		lockVerifyGlobal = false
		lockVerifyFailOn = "drift"
	})
	lockVerifyRepair = false
	lockVerifyGlobal = false
	lockVerifyFailOn = "drift"

	err := runLockVerify(lockVerifyCmd, nil)
	if err == nil {
		t.Fatal("lock verify on drifted entry returned nil; want non-zero exit (exit semantics unchanged)")
	}
	got := stderrString(t)
	if !strings.Contains(got, "edit mode") {
		t.Errorf("stderr = %q, want the edit-mode context", got)
	}
	if !strings.Contains(got, "lock verify --repair") {
		t.Errorf("stderr = %q, want the --repair remedy", got)
	}
	if strings.Contains(got, "remove --force") {
		t.Errorf("stderr = %q, must not suggest remove/add for a sourceless edit-mode skill", got)
	}
}

// TestSyncCheck_EditModeDrift_HintsRepairNotRemoveAdd is the same guard for
// `qvr sync --check`, whose drift report carried the impossible remove/add
// remedy for created skills.
func TestSyncCheck_EditModeDrift_HintsRepairNotRemoveAdd(t *testing.T) {
	setupEditedCreatedSkill(t)
	t.Cleanup(func() {
		syncGlobal, syncDryRun, syncNoScan, syncLocked, syncFrozen, syncCheck = false, false, false, false, false, false
	})
	syncGlobal, syncDryRun, syncNoScan, syncLocked, syncFrozen, syncCheck = false, false, true, false, false, true

	err := runSync(syncCmd, nil)
	if err == nil {
		t.Fatal("sync --check on drifted entry returned nil; want non-zero exit (exit semantics unchanged)")
	}
	got := stderrString(t)
	if !strings.Contains(got, "edited locally (edit mode)") {
		t.Errorf("stderr = %q, want the edit-mode drift wording", got)
	}
	if !strings.Contains(got, "lock verify --repair") {
		t.Errorf("stderr = %q, want the --repair remedy", got)
	}
	if strings.Contains(got, "remove --force") {
		t.Errorf("stderr = %q, must not suggest remove/add for a sourceless edit-mode skill", got)
	}
}

// TestSyncCheck_SharedModeDrift_KeepsRestoreHint pins the boundary: shared
// (consume-mode) entries keep the registry restore remedy — only edit-mode
// entries switched to the --repair wording.
func TestSyncCheck_SharedModeDrift_KeepsRestoreHint(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project := t.TempDir()
	t.Chdir(project)
	resetPrinter(t)
	resetLockResolveFlags(t)

	installBranchPinned(t, "acme", "demo")
	lockPath := model.DefaultLockPath(project, config.Dir(), false)

	// Tamper the recorded hash so the shared entry drifts without touching
	// the (read-only) materialized content.
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	e, err := lock.Get("demo")
	if err != nil {
		t.Fatalf("get demo: %v", err)
	}
	e.SubtreeHash = "sha256:" + strings.Repeat("ab", 32)
	lock.Put(e)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	t.Cleanup(func() {
		syncGlobal, syncDryRun, syncNoScan, syncLocked, syncFrozen, syncCheck = false, false, false, false, false, false
	})
	syncGlobal, syncDryRun, syncNoScan, syncLocked, syncFrozen, syncCheck = false, false, true, false, false, true

	if err := runSync(syncCmd, nil); err == nil {
		t.Fatal("sync --check on tampered entry returned nil; want non-zero exit")
	}
	got := stderrString(t)
	if !strings.Contains(got, "remove demo --force") {
		t.Errorf("stderr = %q, want the registry restore remedy for shared entries", got)
	}
	if strings.Contains(got, "edited locally") {
		t.Errorf("stderr = %q, edit-mode wording must not appear for shared entries", got)
	}
}
