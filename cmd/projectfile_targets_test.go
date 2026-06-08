package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
)

// TestModeLockTargets is the #230 guard: synthesizing a lost qvr.toml's
// default-targets must use the MODE (dominant per-skill target set), never the
// UNION — so a single cursor-only skill alongside three claude skills does not
// widen the default to [claude,cursor] and silently re-route future installs.
func TestModeLockTargets(t *testing.T) {
	lock := model.NewLockFile("")
	for _, n := range []string{"a", "b", "c"} {
		lock.Put(&model.LockEntry{Name: n, Registry: "acme", Targets: []string{"claude"}})
	}
	lock.Put(&model.LockEntry{Name: "odd", Registry: "acme", Targets: []string{"cursor"}})

	got := modeLockTargets(lock)
	if len(got) != 1 || got[0] != "claude" {
		t.Fatalf("modeLockTargets = %v, want [claude] (mode, not union [claude cursor])", got)
	}
}

// TestModeLockTargets_TieIsDeterministic confirms a tie breaks on the joined set
// lexicographically so the synthesized default is stable across runs.
func TestModeLockTargets_TieIsDeterministic(t *testing.T) {
	lock := model.NewLockFile("")
	lock.Put(&model.LockEntry{Name: "a", Registry: "acme", Targets: []string{"cursor"}})
	lock.Put(&model.LockEntry{Name: "b", Registry: "acme", Targets: []string{"claude"}})
	got := modeLockTargets(lock)
	if len(got) != 1 || got[0] != "claude" {
		t.Fatalf("tie should pick lexicographically-smallest set, got %v want [claude]", got)
	}
}

// TestModeLockTargets_IgnoresNonPortable confirms edit/link/local entries (no
// portable coordinate) don't vote on the project's default routing.
func TestModeLockTargets_IgnoresNonPortable(t *testing.T) {
	lock := model.NewLockFile("")
	lock.Put(&model.LockEntry{Name: "shared", Registry: "acme", Targets: []string{"claude"}})
	lock.Put(&model.LockEntry{Name: "edited", Registry: "acme", Mode: model.ModeEdit, Targets: []string{"cursor", "codex"}})
	got := modeLockTargets(lock)
	if len(got) != 1 || got[0] != "claude" {
		t.Fatalf("non-portable entry voted on routing: got %v want [claude]", got)
	}
}

func TestSkillTargetOverride(t *testing.T) {
	// Matches default → no override (bare entry).
	if ov := skillTargetOverride([]string{"claude"}, []string{"claude"}); ov != nil {
		t.Errorf("matching set should yield nil override, got %v", ov)
	}
	// Order/dupes don't matter — set equality.
	if ov := skillTargetOverride([]string{"codex", "claude"}, []string{"claude", "codex"}); ov != nil {
		t.Errorf("same set in different order should yield nil, got %v", ov)
	}
	// Differs → override recorded (normalized/sorted).
	ov := skillTargetOverride([]string{"cursor"}, []string{"claude"})
	if len(ov) != 1 || ov[0] != "cursor" {
		t.Errorf("differing set should record override [cursor], got %v", ov)
	}
}

// TestSyncProjectFileFromLock_RecordsPerSkillOverride is the #228 write-through
// guard: synthesizing qvr.toml from the lock records a per-skill targets override
// (inline table) for a skill whose targets differ from default-targets, and
// leaves a skill matching the default as a bare entry.
func TestSyncProjectFileFromLock_RecordsPerSkillOverride(t *testing.T) {
	resetPrinter(t)
	dir := t.TempDir()
	projPath := filepath.Join(dir, model.ProjectFileName)

	lock := model.NewLockFile(filepath.Join(dir, model.LockFileName))
	// Two skills route to claude, one to cursor → mode default is [claude];
	// the cursor skill becomes the outlier override.
	lock.Put(&model.LockEntry{Name: "foo", Registry: "acme", Ref: "main", Targets: []string{"claude"}})
	lock.Put(&model.LockEntry{Name: "bar", Registry: "acme", Ref: "main", Targets: []string{"claude"}})
	lock.Put(&model.LockEntry{Name: "tdd", Registry: "acme", Ref: "v1", Targets: []string{"cursor"}})

	if err := syncProjectFileFromLock(projPath, lock, nil); err != nil {
		t.Fatalf("syncProjectFileFromLock: %v", err)
	}

	proj, err := model.ReadProjectFile(projPath)
	if err != nil {
		t.Fatalf("read synthesized qvr.toml: %v", err)
	}
	if dt := proj.Project.DefaultTargets; len(dt) != 1 || dt[0] != "claude" {
		t.Fatalf("default-targets = %v, want [claude] (mode, not union)", dt)
	}
	if tg := proj.SkillTargets("acme/foo"); len(tg) != 0 {
		t.Errorf("foo should be bare (matches default), got override %v", tg)
	}
	if tg := proj.SkillTargets("acme/tdd"); len(tg) != 1 || tg[0] != "cursor" {
		t.Errorf("tdd override = %v, want [cursor] (survives regenerate)", tg)
	}

	// On-disk form: the outlier is an inline table, the others bare strings.
	bytesOut, _ := os.ReadFile(projPath)
	if !strings.Contains(string(bytesOut), "targets = ['cursor']") {
		t.Errorf("expected inline override for tdd in:\n%s", bytesOut)
	}
}
