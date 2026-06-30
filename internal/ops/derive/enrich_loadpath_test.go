package derive_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/google/uuid"
)

// writeGlobalLock writes a code-review entry into the user-global lock (under
// QVR_HOME) so EnrichSkillIdentity resolves it via the global fallback —
// codex rows carry no working directory, so the project lock isn't consulted.
func writeGlobalLock(t *testing.T, home string, e *model.LockEntry) {
	t.Helper()
	l := model.NewLockFile(filepath.Join(home, model.LockFileName))
	l.Put(e)
	if err := l.Write(); err != nil {
		t.Fatalf("write global lock: %v", err)
	}
}

// codexSkillLoadRows is a minimal codex session whose single tool call reads a
// skill's SKILL.md at filePath — the native "load" signal. The path is what
// EnrichSkillIdentity verifies against.
func codexSkillLoadRows(sid uuid.UUID, filePath string) []*ops.RawTrace {
	args, _ := json.Marshal(map[string]string{"cmd": "sed -n '1,40p' " + filePath})
	line, _ := json.Marshal(map[string]any{
		"timestamp": "2026-06-02T15:32:03.518Z",
		"type":      "response_item",
		"payload": map[string]any{
			"type":      "function_call",
			"name":      "exec_command",
			"arguments": string(args),
			"call_id":   "call_skill",
		},
	})
	return []*ops.RawTrace{codexRow(sid, 0, string(line))}
}

// seedWorktree creates the on-disk worktree the lock entry pins, with the skill
// living at <worktree>/<entry.Path>/SKILL.md (the standard registry layout).
// Returns the absolute SKILL.md path the agent would load.
func seedWorktree(t *testing.T, e *model.LockEntry) string {
	t.Helper()
	root := registry.WorktreePath(e.Registry, e.Name, registry.ShortSHA(e.Commit))
	dir := filepath.Join(root, e.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	skillMD := filepath.Join(dir, "SKILL.md")
	// The on-disk body MUST match the body skillRows simulates the agent loading
	// ("# code review\n\n## Instructions\n") so the switched-after-run guard
	// (resolvedBodyMatches) confirms this is the version that ran; a
	// frontmatter-only fixture would model an impossible capture.
	if err := os.WriteFile(skillMD, []byte("---\nname: code-review\ndescription: x\n---\n# code review\n\n## Instructions\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return skillMD
}

func crEntry(commit string) *model.LockEntry {
	return &model.LockEntry{
		Name:        "code-review",
		Registry:    "raks",
		Source:      "https://github.com/raks/skills.git",
		Ref:         "v0.2.0",
		Commit:      commit,
		Path:        "skills/code-review",
		SubtreeHash: "sha256:6d478",
		Targets:     []string{"claude"},
	}
}

// TestEnrich_MultiSegRegistry_And_SwitchGuard pins two correctness properties
// for operating multiple versions across agents:
//
//  1. A multi-segment registry ("qa/evolve") resolves: its worktree path carries
//     an extra segment that previously broke store-path proof entirely, so every
//     run silently fell through to current-lock containment.
//  2. The switched-after-run guard: when the install is switched between a run
//     and its (lagged) discovery, the symlink resolves to the WRONG version —
//     identity is withheld rather than mis-stamped, so the run keeps its
//     run-immutable body digest and never conflates with a different version.
//
// QVR_HOME is placed under ".quiver" so worktree paths match the real store
// layout the store-path detector keys on.
func TestEnrich_MultiSegRegistry_And_SwitchGuard(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".quiver")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QVR_HOME", home)

	eA := &model.LockEntry{
		Name: "code-review", Registry: "qa/evolve", Source: "file:///tmp/reg.git",
		Ref: "v0.5.0", Commit: "aaaaaa1000000000000000000000000000000000",
		Path: "skills/code-review", SubtreeHash: "sha256:AAAA", Targets: []string{"claude"},
	}
	writeGlobalLock(t, home, eA)
	seedWorktree(t, eA) // on-disk body == the body skillRows simulates loading

	proj := t.TempDir()
	link := filepath.Join(proj, ".claude", "skills", "code-review")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	skillDirA := filepath.Join(registry.WorktreePath(eA.Registry, eA.Name, registry.ShortSHA(eA.Commit)), eA.Path)
	if err := os.Symlink(skillDirA, link); err != nil {
		t.Fatal(err)
	}

	// (1) Matching load → multi-segment registry resolves AND identity proves.
	rowsA := skillRows(uuid.New(), proj, link)
	dA, _ := derive.DeriveSession(rowsA)
	derive.EnrichSkillIdentity(dA.Spans, rowsA, nil)
	spA := skillSpan(t, dA.Spans)
	if spA.Attributes["skill.registry"] != "qa/evolve" {
		t.Fatalf("multi-segment registry must resolve, got registry=%v", spA.Attributes["skill.registry"])
	}
	if spA.Attributes["skill.version"] != "v0.5.0" {
		t.Errorf("matching load must prove version, got %v", spA.Attributes["skill.version"])
	}

	// (2) Switch the install to a DIFFERENT version+body, then re-derive the run
	// that loaded A. It must NOT inherit B's identity.
	eB := *eA
	eB.Commit = "bbbbbb2000000000000000000000000000000000"
	eB.SubtreeHash = "sha256:BBBB"
	skillDirB := filepath.Join(registry.WorktreePath(eB.Registry, eB.Name, registry.ShortSHA(eB.Commit)), eB.Path)
	if err := os.MkdirAll(skillDirB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDirB, "SKILL.md"),
		[]byte("---\nname: code-review\n---\n# a totally different body that never ran\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeGlobalLock(t, home, &eB)
	os.Remove(link)
	if err := os.Symlink(skillDirB, link); err != nil {
		t.Fatal(err)
	}

	rowsS := skillRows(uuid.New(), proj, link)
	dS, _ := derive.DeriveSession(rowsS)
	derive.EnrichSkillIdentity(dS.Spans, rowsS, nil)
	spS := skillSpan(t, dS.Spans)
	if spS.Attributes["skill.version"] != nil {
		t.Errorf("switched-after-run must withhold version (no conflation), got %v", spS.Attributes["skill.version"])
	}
	if ch, _ := spS.Attributes["skill.content_hash"].(string); ch == "" {
		t.Errorf("withheld load must keep its run-immutable body digest")
	}
}

// TestEnrich_Claude_NoPathIsUnverified pins #149 for claude-shaped evidence:
// a Skill tool call with no load path gets NO identity fields under the
// proof-gated model — skill.version's absence is the unverified signal, and
// the lock pin is a display-time join, never persisted onto the span.
func TestEnrich_Claude_NoPathIsUnverified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	writeGlobalLock(t, home, crEntry("94e539be7d6a01774d723a7c25513af0f070de7b"))

	rows := skillRows(uuid.New(), "", "") // claude Skill tool, no working dir, no base-dir evidence
	d, _ := derive.DeriveSession(rows)
	spans := d.Spans
	derive.EnrichSkillIdentity(spans, rows, nil)

	sp := skillSpan(t, spans)
	for _, k := range []string{"skill.version", "skill.registry", "skill.commit", "skill.subtree_hash"} {
		if v, ok := sp.Attributes[k]; ok {
			t.Errorf("no load path ⇒ no identity may be stamped, but %s=%v", k, v)
		}
	}
	if sp.Attributes["skill.name"] != "code-review" {
		t.Errorf("the bare name should survive, got %v", sp.Attributes["skill.name"])
	}
}

// TestEnrich_Codex_LoadPathInWorktreeIsVerified pins #149 for codex: when the
// loaded file resolves into the locked worktree, full identity is asserted —
// and skill.version is present, which IS the verified signal.
func TestEnrich_Codex_LoadPathInWorktreeIsVerified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	e := crEntry("94e539be7d6a01774d723a7c25513af0f070de7b")
	writeGlobalLock(t, home, e)
	skillMD := seedWorktree(t, e) // the real locked artifact on disk

	rows := codexSkillLoadRows(uuid.New(), skillMD)
	d, _ := derive.DeriveSession(rows)
	spans := d.Spans
	derive.EnrichSkillIdentity(spans, rows, nil)

	sp := skillSpan(t, spans)
	if v, _ := sp.Attributes["skill.version"].(string); v != e.Ref {
		t.Errorf("proven load must stamp skill.version (the verified signal); got %v, attrs=%v", v, sp.Attributes)
	}
	if got := sp.Attributes["skill.commit"]; got != registry.ShortSHA(e.Commit) {
		t.Errorf("verified load should assert the locked commit as a canonical short SHA, got %v", got)
	}
}

// TestEnrich_Codex_ShadowingEjectIsUnverified is the core of #149: a same-named
// skill loaded from a path OUTSIDE the locked worktree (a global eject) must not
// be reported as the locked artifact — no identity fields are stamped, so
// skill.version is absent (the unverified signal).
func TestEnrich_Codex_ShadowingEjectIsUnverified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	e := crEntry("94e539be7d6a01774d723a7c25513af0f070de7b")
	writeGlobalLock(t, home, e)
	seedWorktree(t, e) // the locked copy exists...

	// ...but the agent loaded a DIFFERENT copy: a global eject under ~/.claude.
	ejectMD := filepath.Join(home, ".claude", "skills", "code-review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(ejectMD), 0o755); err != nil {
		t.Fatalf("mkdir eject: %v", err)
	}
	if err := os.WriteFile(ejectMD, []byte("---\nname: code-review\ndescription: drifted\n---\n"), 0o644); err != nil {
		t.Fatalf("write eject: %v", err)
	}

	rows := codexSkillLoadRows(uuid.New(), ejectMD)
	d, _ := derive.DeriveSession(rows)
	spans := d.Spans
	derive.EnrichSkillIdentity(spans, rows, nil)

	sp := skillSpan(t, spans)
	if v, ok := sp.Attributes["skill.version"]; ok {
		t.Errorf("an eject outside the worktree must carry no skill.version (unverified); got %v", v)
	}
	for _, k := range []string{"skill.commit", "skill.registry", "skill.subtree_hash"} {
		if _, ok := sp.Attributes[k]; ok {
			t.Errorf("must not attest %s for a copy the agent provably did not load: %v", k, sp.Attributes[k])
		}
	}
	if sp.Attributes["skill.name"] != "code-review" {
		t.Errorf("the bare name should survive, got %v", sp.Attributes["skill.name"])
	}
}

// TestEscalateUnresolvedIdentity pins the lock-independent escalation: a run
// resolved off-checkout (codex commit-only, or claude body-digest-only) inherits
// the full identity another session already proved for the SAME run-immutable
// evidence — so a version labels uniformly regardless of the current checkout.
// An already-full span is left untouched.
func TestEscalateUnresolvedIdentity(t *testing.T) {
	full := &model.LockEntry{
		Registry: "qa/evolve", Ref: "v0.5.0", Commit: "2c20399",
		SubtreeHash: "sha256:afcdd32b",
	}
	byCommit := func(c string) *model.LockEntry {
		if c == "2c20399" {
			return full
		}
		return nil
	}
	byContent := func(h string) *model.LockEntry {
		if h == "sha256:body05" {
			return full
		}
		return nil
	}

	spans := []derive.Span{
		// codex off-checkout: commit-only, no subtree.
		{Kind: derive.KindSkill, Attributes: map[string]any{"skill.name": "slugify-title", "skill.commit": "2c20399"}},
		// claude off-checkout: body-digest content_hash, no commit/subtree.
		{Kind: derive.KindSkill, Attributes: map[string]any{"skill.name": "slugify-title", "skill.content_hash": "sha256:body05"}},
		// already full: must not be re-resolved.
		{Kind: derive.KindSkill, Attributes: map[string]any{"skill.name": "x", "skill.version": "v9", "skill.subtree_hash": "sha256:keep"}},
		// unknown evidence: stays unresolved.
		{Kind: derive.KindSkill, Attributes: map[string]any{"skill.name": "y", "skill.commit": "deadbee"}},
	}
	derive.EscalateUnresolvedIdentity(spans, byCommit, byContent)

	if v := spans[0].Attributes["skill.version"]; v != "v0.5.0" {
		t.Errorf("commit-only span must escalate to ref, got version=%v", v)
	}
	if s := spans[0].Attributes["skill.subtree_hash"]; s != "sha256:afcdd32b" {
		t.Errorf("escalated span must carry the proven subtree, got %v", s)
	}
	if v := spans[1].Attributes["skill.version"]; v != "v0.5.0" {
		t.Errorf("body-digest span must escalate via content_hash, got version=%v", v)
	}
	if v := spans[2].Attributes["skill.version"]; v != "v9" {
		t.Errorf("already-full span must be untouched, got version=%v", v)
	}
	if _, ok := spans[3].Attributes["skill.version"]; ok {
		t.Errorf("unresolvable evidence must stay unescalated")
	}
}
