package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
)

// installBranchPinned registers regName → a fresh remote carrying skill `name`
// on main and installs it pinned to the branch (entry.Ref="main"). Returns the
// remote path so a test can advance it. Reuses seedTaggedPullRemote from
// pull_test.go (the extra tag it creates is harmless here).
func installBranchPinned(t *testing.T, regName, name string) string {
	t.Helper()
	gc := git.NewGoGitClient()
	mgr := newRegistryManager(gc)
	remote := seedTaggedPullRemote(t, name, "v1.0.0")
	if _, err := mgr.Add(context.Background(), regName, remote); err != nil {
		t.Fatalf("registry add %s: %v", regName, err)
	}
	project, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	inst := skill.NewInstaller(mgr, git.NewGoGitWorktree(), gc)
	if _, err := inst.Install(skill.InstallRequest{
		Skill:       name + "@main",
		Targets:     []string{"claude"},
		ProjectRoot: project,
	}); err != nil {
		t.Fatalf("install %s@main: %v", name, err)
	}
	return remote
}

// advanceRemoteMain clones the remote, rewrites the skill's SKILL.md, commits,
// and pushes main — advancing the branch tip. Returns the new full commit hash.
func advanceRemoteMain(t *testing.T, remote, name, marker string) string {
	t.Helper()
	work := t.TempDir()
	r, err := gogit.PlainClone(work, false, &gogit.CloneOptions{
		URL:           remote,
		ReferenceName: plumbing.NewBranchReferenceName("main"),
	})
	if err != nil {
		t.Fatalf("clone for advance: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	rel := "skills/" + name + "/SKILL.md"
	body := "---\nname: " + name + "\ndescription: advanced (" + marker + ")\n---\n# " + name + "\n" + marker + "\n"
	if err := os.WriteFile(work+"/"+rel, []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := wt.Add(rel); err != nil {
		t.Fatalf("git add: %v", err)
	}
	h, err := wt.Commit("advance "+marker, &gogit.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := r.Push(&gogit.PushOptions{RemoteName: "origin", RefSpecs: []gogitcfg.RefSpec{
		"refs/heads/main:refs/heads/main",
	}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	return h.String()
}

func resetLockResolveFlags(t *testing.T) {
	t.Helper()
	// cmd.Context() is nil when RunE is invoked directly (cobra sets it during
	// Execute); give it a real context so the fetch path doesn't panic.
	lockCmd.SetContext(context.Background())
	t.Cleanup(func() {
		lockResolvePackage = ""
		lockResolveDryRun = false
		lockResolveGlobal = false
		lockCmd.SetContext(context.Background())
	})
	lockResolvePackage = ""
	lockResolveDryRun = false
	lockResolveGlobal = false
}

func lockEntryCommit(t *testing.T, lockPath, name string) string {
	t.Helper()
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	e, err := lock.Get(name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return e.Commit
}

// TestRunLock_RepinsAdvancedBranch is the core re-resolve: after the upstream
// branch advances, `qvr lock` re-pins the recorded commit to the new tip and
// invalidates the content hash (the next sync refills it).
func TestRunLock_RepinsAdvancedBranch(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	remote := installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	c1 := lockEntryCommit(t, lockPath, "demo")

	newHash := advanceRemoteMain(t, remote, "demo", "v2")
	if newHash == c1 {
		t.Fatal("advance produced the same commit")
	}

	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock: %v", err)
	}

	got := lockEntryCommit(t, lockPath, "demo")
	if got != newHash {
		t.Errorf("commit = %s, want re-pinned %s", got, newHash)
	}
	// The hash is recomputed from the bare clone's objects (no checkout), so
	// the re-pinned entry is immediately verifiable — not left empty.
	lock, _ := model.ReadLockFile(lockPath)
	e, _ := lock.Get("demo")
	if !strings.HasPrefix(e.SubtreeHash, "sha256:") {
		t.Errorf("re-pin should recompute subtreeHash from objects, got %q", e.SubtreeHash)
	}
}

// TestRunLock_ThenSyncIsGreen is the end-to-end contract: a re-pin recomputes
// the content hash from objects, so the immediately following `qvr sync`
// materialises the new commit and `qvr sync --check` reports in-sync — no
// integrity failure, no manual `qvr lock upgrade` step in between.
func TestRunLock_ThenSyncIsGreen(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetSyncModeFlags(t)
	resetPrinter(t)

	remote := installBranchPinned(t, "acme", "demo")
	advanceRemoteMain(t, remote, "demo", "v2")

	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock: %v", err)
	}
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync after re-pin should be green: %v", err)
	}
	syncCheck = true
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync --check after re-pin+sync should be green: %v", err)
	}
}

// TestRunLock_DryRunDoesNotWrite confirms --dry-run previews the re-pin but
// leaves qvr.lock byte-identical.
func TestRunLock_DryRunDoesNotWrite(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	remote := installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	advanceRemoteMain(t, remote, "demo", "v2")

	before, _ := os.ReadFile(lockPath)
	lockResolveDryRun = true
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock --dry-run: %v", err)
	}
	after, _ := os.ReadFile(lockPath)
	if string(before) != string(after) {
		t.Errorf("--dry-run mutated qvr.lock:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestRunLock_FromTomlRemovesDeletedSkill is the #229 guard: deleting a skill
// from qvr.toml and running `qvr lock --from-toml` removes the lock entry AND
// tears down its install (symlink + worktree) — the toml-authoritative deletion
// the additive `qvr sync` and refs-only old `--from-toml` both failed to honor.
func TestRunLock_FromTomlRemovesDeletedSkill(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)

	// The skill is installed: lock entry + symlink exist.
	if _, err := lockEntryGet(t, lockPath, "demo"); err != nil {
		t.Fatalf("precondition: demo should be installed: %v", err)
	}
	link := filepath.Join(project, ".claude", "skills", "demo")
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("precondition: symlink should exist: %v", err)
	}

	// qvr.toml EXISTS but does not declare acme/demo → it's a deletion.
	projPath := model.DefaultProjectPath(project)
	if err := os.WriteFile(projPath, []byte("[project]\ndefault-targets = ['claude']\n"), 0o644); err != nil {
		t.Fatalf("write qvr.toml: %v", err)
	}

	lockResolveFromToml = true
	t.Cleanup(func() { lockResolveFromToml = false })
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock --from-toml: %v", err)
	}

	// Lock entry gone, symlink torn down.
	lock, _ := model.ReadLockFile(lockPath)
	if _, err := lock.Get("demo"); err == nil {
		t.Errorf("demo should be removed from the lock (absent from qvr.toml)")
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("symlink should be torn down, stat err = %v", err)
	}
}

// TestRunLock_FromTomlAbsentTomlIsNoop is the safety guard for the deletion
// path: an ABSENT qvr.toml (the lock's self-sufficient default state) must NOT
// be read as "nothing declared" and tear down every skill. It's a no-op.
func TestRunLock_FromTomlAbsentTomlIsNoop(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)

	// No qvr.toml on disk.
	if _, err := os.Stat(model.DefaultProjectPath(project)); !os.IsNotExist(err) {
		t.Fatalf("precondition: qvr.toml must be absent, stat err = %v", err)
	}

	lockResolveFromToml = true
	t.Cleanup(func() { lockResolveFromToml = false })
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock --from-toml: %v", err)
	}

	lock, _ := model.ReadLockFile(lockPath)
	if _, err := lock.Get("demo"); err != nil {
		t.Errorf("absent qvr.toml must be a no-op; demo should survive, got %v", err)
	}
}

// TestRunLock_RecordsProject confirms `qvr lock` re-asserts the project in
// projects.json (mirroring add/sync/remove), so reachability and the
// shared-worktree teardown gate (#232) stay accurate for a project last touched
// via lock.
func TestRunLock_RecordsProject(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)

	// Simulate the record being lost (e.g. pruned), then run `qvr lock`.
	registry.ForgetProject(lockPath)
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock: %v", err)
	}

	pf, _ := registry.ReadProjects()
	if _, ok := pf.Projects[lockPath]; !ok {
		t.Errorf("`qvr lock` should record the project in projects.json")
	}
}

func lockEntryGet(t *testing.T, lockPath, name string) (*model.LockEntry, error) {
	t.Helper()
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	return lock.Get(name)
}

// TestRunLock_PackageSelectsSingleSkill confirms -P re-pins only the named
// skill, leaving siblings untouched even though both advanced upstream.
func TestRunLock_PackageSelectsSingleSkill(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	remoteA := installBranchPinned(t, "acme", "demo")
	remoteB := installBranchPinned(t, "beta", "other")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	demo0 := lockEntryCommit(t, lockPath, "demo")
	other0 := lockEntryCommit(t, lockPath, "other")

	demoNew := advanceRemoteMain(t, remoteA, "demo", "v2")
	advanceRemoteMain(t, remoteB, "other", "v2")

	lockResolvePackage = "demo"
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock -P demo: %v", err)
	}

	if got := lockEntryCommit(t, lockPath, "demo"); got != demoNew {
		t.Errorf("demo commit = %s, want re-pinned %s", got, demoNew)
	}
	if got := lockEntryCommit(t, lockPath, "other"); got != other0 {
		t.Errorf("other commit = %s, want untouched %s", got, other0)
	}
	if demo0 == demoNew {
		t.Fatal("demo did not actually advance")
	}
}

// writeProjTomlFor declares the named entry's coordinate at the given ref in
// qvr.toml, creating the file if needed. Returns the coordinate written.
func writeProjTomlFor(t *testing.T, project, lockPath, name, ref string) string {
	t.Helper()
	e, err := lockEntryGet(t, lockPath, name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	coord := model.SkillCoordinate(e)
	if coord == "" {
		t.Fatalf("entry %s has no qvr.toml coordinate", name)
	}
	proj, perr := model.ReadProjectFile(model.DefaultProjectPath(project))
	if perr != nil {
		t.Fatalf("read qvr.toml: %v", perr)
	}
	proj.PutSkill(coord, ref)
	if werr := proj.Write(); werr != nil {
		t.Fatalf("write qvr.toml: %v", werr)
	}
	return coord
}

// TestRunLock_FromToml_RefRewriteSameCommit_ReportsRefUpdated is the #246
// reporting guard: hand-editing a qvr.toml ref to one that resolves to the
// same commit (here tag v1.0.0 → same tip as main) is a lock mutation and
// must be reported as such — not "unchanged".
func TestRunLock_FromToml_RefRewriteSameCommit_ReportsRefUpdated(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	installBranchPinned(t, "acme", "demo") // ref=main; tag v1.0.0 at the same commit
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	c0 := lockEntryCommit(t, lockPath, "demo")
	e0, _ := lockEntryGet(t, lockPath, "demo")
	hash0 := e0.SubtreeHash

	writeProjTomlFor(t, project, lockPath, "demo", "v1.0.0")

	lockResolveFromToml = true
	t.Cleanup(func() { lockResolveFromToml = false })
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock --from-toml: %v", err)
	}

	e, err := lockEntryGet(t, lockPath, "demo")
	if err != nil {
		t.Fatalf("get demo: %v", err)
	}
	if e.Ref != "v1.0.0" {
		t.Errorf("ref = %q, want adopted %q (#246)", e.Ref, "v1.0.0")
	}
	if e.Commit != c0 {
		t.Errorf("commit = %s, want untouched %s (same-commit ref rewrite)", e.Commit, c0)
	}
	if e.SubtreeHash != hash0 {
		t.Errorf("subtreeHash churned on a same-commit ref rewrite: %q → %q", hash0, e.SubtreeHash)
	}
	outBuf, ok := printer.Out.(interface{ String() string })
	if !ok {
		t.Fatalf("printer.Out is not a stringer; got %T", printer.Out)
	}
	if got := outBuf.String(); !strings.Contains(got, "ref main → v1.0.0 (same commit") {
		t.Errorf("output = %q, want a 'ref main → v1.0.0 (same commit …)' line, not 'unchanged' (#246)", got)
	}
}

// TestRunLock_FromToml_UntouchedBranchEntryNotRepinned is the #246 scope
// guard: --from-toml reconciles intent edits only. An entry whose qvr.toml
// line matches the lock's ref keeps its pinned commit even when the upstream
// branch advanced — fast-forwarding is bare `qvr lock`'s job.
func TestRunLock_FromToml_UntouchedBranchEntryNotRepinned(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	remote := installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	c0 := lockEntryCommit(t, lockPath, "demo")

	writeProjTomlFor(t, project, lockPath, "demo", "main") // declared at its current ref
	newHash := advanceRemoteMain(t, remote, "demo", "v2")
	if newHash == c0 {
		t.Fatal("advance produced the same commit")
	}

	lockResolveFromToml = true
	t.Cleanup(func() { lockResolveFromToml = false })
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock --from-toml: %v", err)
	}
	if got := lockEntryCommit(t, lockPath, "demo"); got != c0 {
		t.Errorf("--from-toml fast-forwarded an untouched entry: commit = %s, want %s (#246)", got, c0)
	}

	// Bare `qvr lock` remains the explicit re-pin verb on the same fixture.
	lockResolveFromToml = false
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("bare runLock: %v", err)
	}
	if got := lockEntryCommit(t, lockPath, "demo"); got != newHash {
		t.Errorf("bare lock should re-pin: commit = %s, want %s", got, newHash)
	}
}

// TestRunLock_FromToml_DryRunRefRewriteDoesNotWrite: a --dry-run --from-toml
// same-commit ref rewrite reports would-ref-update and leaves the lock bytes
// untouched.
func TestRunLock_FromToml_DryRunRefRewriteDoesNotWrite(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock bytes: %v", err)
	}

	writeProjTomlFor(t, project, lockPath, "demo", "v1.0.0")

	lockResolveFromToml = true
	lockResolveDryRun = true
	t.Cleanup(func() { lockResolveFromToml = false })
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock --from-toml --dry-run: %v", err)
	}

	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("re-read lock bytes: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("--dry-run mutated the lock:\nbefore=%s\nafter=%s", before, after)
	}
	outBuf, ok := printer.Out.(interface{ String() string })
	if !ok {
		t.Fatalf("printer.Out is not a stringer; got %T", printer.Out)
	}
	if got := outBuf.String(); !strings.Contains(got, "would update ref main → v1.0.0") {
		t.Errorf("output = %q, want a would-ref-update line", got)
	}
}
