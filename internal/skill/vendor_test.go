package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/canonical"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
)

// assertRealDirWithSkill asserts path is a real directory (not a symlink)
// holding a SKILL.md — the vendoring contract for the canonical target.
func assertRealDirWithSkill(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is a symlink; vendoring must produce a real directory", path)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
	if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
		t.Errorf("vendored dir missing SKILL.md: %v", err)
	}
}

// TestInstallLocal_Vendor vendors a local folder: the canonical target becomes a
// real in-repo directory (not a store symlink), the lock entry is mode:vendor
// with VendorPath set, and the bytes are a snapshot independent of the source.
func TestInstallLocal_Vendor(t *testing.T) {
	h := newHarness(t)
	src := writeLocalSkill(t, "my-skill", "local dev skill")

	result, err := h.installer.InstallLocal(src, skill.InstallRequest{
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Vendor:      true,
	})
	if err != nil {
		t.Fatalf("InstallLocal --vendor: %v", err)
	}

	vendorDir := filepath.Join(h.project, ".claude/skills/my-skill")
	assertRealDirWithSkill(t, vendorDir)
	if result.Worktree != vendorDir {
		t.Errorf("result.Worktree = %s, want vendored dir %s", result.Worktree, vendorDir)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("my-skill")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if !entry.IsVendor() {
		t.Errorf("entry.IsVendor() = false; mode = %q", entry.Mode)
	}
	if entry.VendorPath != ".claude/skills/my-skill" {
		t.Errorf("VendorPath = %q, want .claude/skills/my-skill", entry.VendorPath)
	}
	if entry.SubtreeHash == "" {
		t.Error("vendored entry has empty SubtreeHash")
	}

	// Snapshot, not a live link: editing the source must not change the vendored
	// copy (its bytes were materialized into the repo).
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("mutated"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(vendorDir, "SKILL.md"))
	if string(got) == "mutated" {
		t.Error("vendored copy changed when source was edited — not a snapshot")
	}
}

// TestInstall_VendorRegistry vendors a registry skill: real in-repo dir with the
// skill at its root, mode:vendor, and provenance preserved from the install.
func TestInstall_VendorRegistry(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Vendor:      true,
	}); err != nil {
		t.Fatalf("Install --vendor: %v", err)
	}

	vendorDir := filepath.Join(h.project, ".claude/skills/code-review")
	assertRealDirWithSkill(t, vendorDir)

	lock, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if !entry.IsVendor() {
		t.Errorf("entry.IsVendor() = false; mode = %q", entry.Mode)
	}
	if entry.AuthorIdentity() != "Test <t@t>" {
		t.Errorf("vendored entry lost provenance: commitAuthor = %q", entry.AuthorIdentity())
	}
	// The re-sealed hash must match a fresh recomputation over the in-repo dir.
	got, err := canonical.HashSubtreeFromDisk(vendorDir)
	if err != nil {
		t.Fatalf("hash vendored dir: %v", err)
	}
	if got != entry.SubtreeHash {
		t.Errorf("SubtreeHash %q != disk hash %q", entry.SubtreeHash, got)
	}
}

// TestVendor_MultiTargetSiblingSymlinks: the alphabetical-first target is the
// real dir; other targets are RELATIVE symlinks to it (so the layout survives a
// git clone to a different absolute path).
func TestVendor_MultiTargetSiblingSymlinks(t *testing.T) {
	h := newHarness(t)
	src := writeLocalSkill(t, "my-skill", "local dev skill")

	if _, err := h.installer.InstallLocal(src, skill.InstallRequest{
		Targets:     []string{"claude", "cursor"},
		ProjectRoot: h.project,
		Vendor:      true,
	}); err != nil {
		t.Fatalf("InstallLocal --vendor multi-target: %v", err)
	}

	// claude sorts first → canonical real dir.
	assertRealDirWithSkill(t, filepath.Join(h.project, ".claude/skills/my-skill"))

	// cursor (.agents/skills) → relative symlink to the canonical.
	sibling := filepath.Join(h.project, ".agents/skills/my-skill")
	info, err := os.Lstat(sibling)
	if err != nil {
		t.Fatalf("lstat sibling: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("sibling %s should be a symlink", sibling)
	}
	target, _ := os.Readlink(sibling)
	if filepath.IsAbs(target) {
		t.Errorf("sibling symlink target %q is absolute; want relative for clone-portability", target)
	}
	// It must resolve to the canonical real dir.
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling symlink does not resolve: %v", err)
	}
}

// TestVendor_TravelsWithoutStore proves the whole point of vendoring: with the
// store wiped (as on a teammate's fresh clone), the skill still reads and
// `qvr sync` reconciles without error.
func TestVendor_TravelsWithoutStore(t *testing.T) {
	h := newHarness(t)
	src := writeLocalSkill(t, "my-skill", "local dev skill")

	if _, err := h.installer.InstallLocal(src, skill.InstallRequest{
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Vendor:      true,
	}); err != nil {
		t.Fatalf("InstallLocal --vendor: %v", err)
	}

	// Nuke the entire store — a teammate cloning the repo has none of it.
	if err := os.RemoveAll(registry.WorktreesRoot()); err != nil {
		t.Fatalf("wipe store: %v", err)
	}

	vendorDir := filepath.Join(h.project, ".claude/skills/my-skill")
	assertRealDirWithSkill(t, vendorDir) // still there — it lives in the repo

	lock, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	rec := skill.NewReconciler(h.installer)
	res, err := rec.Reconcile(lock, h.project, h.home, skill.ReconcileOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("reconcile reported errors with store gone: %v", res.Errors)
	}
	assertRealDirWithSkill(t, vendorDir)
}

// TestVendor_Remove tears a vendored skill down without --force: the in-repo dir,
// symlinks, and lock entry all go away (vendored files are reproducible).
func TestVendor_Remove(t *testing.T) {
	h := newHarness(t)
	src := writeLocalSkill(t, "my-skill", "local dev skill")

	if _, err := h.installer.InstallLocal(src, skill.InstallRequest{
		Targets:     []string{"claude", "cursor"},
		ProjectRoot: h.project,
		Vendor:      true,
	}); err != nil {
		t.Fatalf("InstallLocal --vendor: %v", err)
	}

	vendorDir := filepath.Join(h.project, ".claude/skills/my-skill")
	sibling := filepath.Join(h.project, ".agents/skills/my-skill")

	if err := h.installer.Remove("my-skill", skill.InstallRequest{ProjectRoot: h.project}); err != nil {
		t.Fatalf("Remove vendored (no --force): %v", err)
	}
	if _, err := os.Lstat(vendorDir); !os.IsNotExist(err) {
		t.Errorf("vendored dir still present after remove: %v", err)
	}
	if _, err := os.Lstat(sibling); !os.IsNotExist(err) {
		t.Errorf("sibling symlink still present after remove: %v", err)
	}
	lock, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if _, err := lock.Get("my-skill"); err == nil {
		t.Error("lock entry still present after remove")
	}
}

// TestEdit_RefusesVendored: a vendored skill is already editable real files, so
// `qvr edit` (EjectToTarget) refuses it rather than producing a confusing
// clobber error.
func TestEdit_RefusesVendored(t *testing.T) {
	h := newHarness(t)
	src := writeLocalSkill(t, "my-skill", "local dev skill")

	if _, err := h.installer.InstallLocal(src, skill.InstallRequest{
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Vendor:      true,
	}); err != nil {
		t.Fatalf("InstallLocal --vendor: %v", err)
	}

	lock, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	entry, _ := lock.Get("my-skill")
	if _, err := skill.EjectToTarget(skill.EjectRequest{Entry: entry, ProjectRoot: h.project}); err == nil {
		t.Fatal("EjectToTarget on a vendored entry should error")
	}
}

// TestMaterializeFromDisk_RefusesEscapingSymlink guards the --local disk
// materialization escape check, including a BARE ".." target (which cleans to
// ".." with no "../" prefix and previously slipped through).
func TestMaterializeFromDisk_RefusesEscapingSymlink(t *testing.T) {
	for _, target := range []string{"..", "../outside", "/etc/passwd"} {
		t.Run(target, func(t *testing.T) {
			src := t.TempDir()
			if err := os.WriteFile(filepath.Join(src, "SKILL.md"),
				[]byte("---\nname: x\ndescription: y\n---\nbody\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(src, "escape")); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "out")
			err := (&skill.Materializer{}).MaterializeFromDisk(src, dest)
			if err == nil || !strings.Contains(err.Error(), "escaping target") {
				t.Errorf("target %q: want escaping-target rejection, got %v", target, err)
			}
		})
	}
}
