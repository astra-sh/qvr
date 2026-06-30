package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/skill"
)

// Two registries each expose a skill named "shared" with DISTINCT content, so a
// silent registry repoint shows up as swapped bytes on disk, not just a changed
// lock field. The marker line is what the assertions read back.
const sharedSkillAlpha = `---
name: shared
description: same name across multiple registries
---

# shared
MARKER=ALPHA
`

const sharedSkillBeta = `---
name: shared
description: same name across multiple registries
---

# shared
MARKER=BETA
`

// installedSkillBody reads the materialized SKILL.md for the lock entry named
// localName, following the same worktree-path derivation the read-side helpers
// use. It's how the tests below prove the install content (not just the lock
// field) tracks the pinned registry.
func installedSkillBody(t *testing.T, projectRoot, localName string) string {
	t.Helper()
	lock, err := model.ReadLockFile(filepath.Join(projectRoot, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get(localName)
	if err != nil {
		t.Fatalf("lock get %q: %v", localName, err)
	}
	skillMD := filepath.Join(skill.EntryWorktreePath(entry), entry.Path, "SKILL.md")
	data, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("read installed SKILL.md (%s): %v", skillMD, err)
	}
	return string(data)
}

// TestInstall_BareReAdd_HonorsLockedRegistry is the regression: once "shared"
// is pinned to "beta" (the alphabetically-SECOND registry, so an
// alphabetical-first re-resolve would land on "alpha"), a bare re-add — no
// --registry — must keep the locked registry and its content, not silently
// repoint to "alpha". Pre-fix the lock's registry field was rewritten to
// "alpha" and the worktree content was swapped, with exit 0.
func TestInstall_BareReAdd_HonorsLockedRegistry(t *testing.T) {
	h := newHarness(t)
	remoteAlpha := seedRemote(t, map[string]string{"shared": sharedSkillAlpha})
	remoteBeta := seedRemote(t, map[string]string{"shared": sharedSkillBeta})
	h.addRegistry(t, "alpha", remoteAlpha)
	h.addRegistry(t, "beta", remoteBeta)

	// Pin to beta explicitly.
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Registry:    "beta",
	}); err != nil {
		t.Fatalf("install --registry beta: %v", err)
	}
	if got := installedSkillBody(t, h.project, "shared"); got != sharedSkillBeta {
		t.Fatalf("after pinned install, content is not beta's:\n%s", got)
	}

	// Bare re-add — no --registry. Must honor the lock's beta pin.
	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("bare re-add: %v", err)
	}
	if result.Registry != "beta" {
		t.Errorf("Registry = %q after bare re-add, want beta (locked pin honored, not alphabetical alpha)", result.Registry)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no ambiguity warning (lock already disambiguated), got %v", result.Warnings)
	}
	if got := installedSkillBody(t, h.project, "shared"); got != sharedSkillBeta {
		t.Errorf("bare re-add swapped installed content away from beta:\n%s", got)
	}
}

// TestInstall_BareReAdd_HonorsLockedSourceForMigratedFork covers the impact
// scenario: a skill migrated to a fork carries an empty Registry but a Source
// URL (the `qvr publish --fork --migrate` shape). A bare re-add must resolve
// back through that Source to its own registry, not the alphabetical-first
// same-named one.
func TestInstall_BareReAdd_HonorsLockedSourceForMigratedFork(t *testing.T) {
	h := newHarness(t)
	remoteAlpha := seedRemote(t, map[string]string{"shared": sharedSkillAlpha})
	remoteBeta := seedRemote(t, map[string]string{"shared": sharedSkillBeta})
	h.addRegistry(t, "alpha", remoteAlpha)
	h.addRegistry(t, "beta", remoteBeta)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Registry:    "beta",
	}); err != nil {
		t.Fatalf("install --registry beta: %v", err)
	}

	// Simulate the migrated-fork lock shape: Registry cleared, Source retained.
	lockPath := filepath.Join(h.project, model.LockFileName)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("shared")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if entry.Source == "" {
		t.Fatalf("expected a recorded Source on the lock entry, got empty")
	}
	entry.Registry = ""
	lock.Put(entry)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	// Bare re-add — must resolve via Source back to beta, not pick alpha.
	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("bare re-add (migrated fork): %v", err)
	}
	if result.Registry != "beta" {
		t.Errorf("Registry = %q after bare re-add, want beta (resolved via Source)", result.Registry)
	}
	if got := installedSkillBody(t, h.project, "shared"); got != sharedSkillBeta {
		t.Errorf("bare re-add swapped installed content away from beta:\n%s", got)
	}
}

// TestInstall_BareReAdd_ExplicitRegistryRepoints confirms the sanctioned
// escape hatch still works: passing --registry alpha to a beta-pinned skill
// overrides the lock pin and repoints to alpha. (A different ref would trip the
// conflict guard; same-ref same-content here is the in-place repoint case.)
func TestInstall_BareReAdd_ExplicitRegistryRepoints(t *testing.T) {
	h := newHarness(t)
	remoteAlpha := seedRemote(t, map[string]string{"shared": sharedSkillAlpha})
	remoteBeta := seedRemote(t, map[string]string{"shared": sharedSkillBeta})
	h.addRegistry(t, "alpha", remoteAlpha)
	h.addRegistry(t, "beta", remoteBeta)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Registry:    "beta",
	}); err != nil {
		t.Fatalf("install --registry beta: %v", err)
	}

	// Explicit --registry alpha + --force is the sanctioned repoint.
	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Registry:    "alpha",
		Force:       true,
	})
	if err != nil {
		t.Fatalf("explicit --registry alpha repoint: %v", err)
	}
	if result.Registry != "alpha" {
		t.Errorf("Registry = %q, want alpha (explicit --registry overrides the lock pin)", result.Registry)
	}
	if got := installedSkillBody(t, h.project, "shared"); got != sharedSkillAlpha {
		t.Errorf("explicit repoint did not swap content to alpha:\n%s", got)
	}
}
