package derive_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// claudeSkillBodyRows is a claude skill load whose harness-injected (isMeta)
// base-directory record carries baseDir on its first line and body after it —
// the verbatim SKILL.md the run loaded. baseDir need not exist on disk: the
// content coordinate is taken from the captured body, not from reading the path.
func claudeSkillBodyRows(sid uuid.UUID, baseDir, body string) []*ops.RawTrace {
	inject, _ := json.Marshal(map[string]any{
		"type": "user", "isMeta": true, "timestamp": "2026-06-02T00:00:03.000Z",
		"message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "Base directory for this skill: " + baseDir + "\n" + body},
		}},
	})
	return []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"review my code"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[`+
			`{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{"skill":"code-review"}}]}}`),
		row(sid, 2, `{"type":"user","timestamp":"2026-06-02T00:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_skill","content":"Launching skill: code-review"}]}}`),
		row(sid, 3, string(inject)),
	}
}

// codexPathLoadWithBody is a codex by-path skill load whose function_call reads
// filePath and whose function_call_output returns body — the verbatim SKILL.md
// content. The body is the run-time content coordinate for a by-path load.
func codexPathLoadWithBody(sid uuid.UUID, filePath, body string) []*ops.RawTrace {
	args, _ := json.Marshal(map[string]string{"cmd": "cat " + filePath})
	call, _ := json.Marshal(map[string]any{
		"timestamp": "2026-06-02T15:32:03.518Z", "type": "response_item",
		"payload": map[string]any{
			"type": "function_call", "name": "exec_command",
			"arguments": string(args), "call_id": "call_skill",
		},
	})
	out, _ := json.Marshal(map[string]any{
		"timestamp": "2026-06-02T15:32:04.000Z", "type": "response_item",
		"payload": map[string]any{
			"type": "function_call_output", "call_id": "call_skill", "output": body,
		},
	})
	return []*ops.RawTrace{codexRow(sid, 0, string(call)), codexRow(sid, 1, string(out))}
}

func contentHash(t *testing.T, sp *derive.Span) string {
	t.Helper()
	h, _ := sp.Attributes[derive.SkillContentHashKey].(string)
	return h
}

// TestContentHash_ClaudeBodyIsRunTime is the core of the cohort-attribution fix:
// the content coordinate is the digest of the body the run LOADED (captured in
// the transcript), not a re-hash of whatever the load path points at now. The
// base directory here resolves to NOTHING on disk, yet the run is still
// coordinated — proving the hash never touches disk-at-discover.
func TestContentHash_ClaudeBodyIsRunTime(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	gone := filepath.Join(t.TempDir(), ".claude", "skills", "code-review") // never created
	body := "---\nname: code-review\ndescription: v1\n---\n# review\nkeep punctuation\n"
	rows := claudeSkillBodyRows(uuid.New(), gone, body)
	d, _ := derive.DeriveSession(rows)
	derive.EnrichSkillIdentity(d.Spans, rows, nil)
	sp := skillSpan(t, d.Spans)

	if got := contentHash(t, sp); got == "" || got[:7] != "sha256:" {
		t.Fatalf("a loaded body must coordinate the run; content_hash = %q", got)
	}
	if _, ok := sp.Attributes["skill.subtree_hash"]; ok {
		t.Error("an unproven symlink load carries no subtree_hash attestation, only the run-time content_hash")
	}
}

// TestContentHash_TracksBodyNotDiscoverTiming is the evolution-loop guarantee:
// two runs of DIFFERENT bodies bucket apart and two runs of the SAME body bucket
// together — regardless of when each was discovered. This is exactly the
// switch-then-discover sequence that previously mis-attributed every run to the
// current on-disk version.
func TestContentHash_TracksBodyNotDiscoverTiming(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	base := filepath.Join(t.TempDir(), ".claude", "skills", "code-review")
	v1 := "---\nname: code-review\n---\n# review\nkeep punctuation\n"
	v2 := "---\nname: code-review\n---\n# review\nstrip punctuation too\n"

	hashFor := func(body string) string {
		rows := claudeSkillBodyRows(uuid.New(), base, body)
		d, _ := derive.DeriveSession(rows)
		derive.EnrichSkillIdentity(d.Spans, rows, nil)
		return contentHash(t, skillSpan(t, d.Spans))
	}

	a1, a2, b := hashFor(v1), hashFor(v1), hashFor(v2)
	if a1 == "" || a1 != a2 {
		t.Errorf("same body must yield the same coordinate: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("different bodies must bucket apart, both got %q", a1)
	}
}

// TestContentHash_ClaudeStripsArgsTrailer is the ground-truth case from the
// real harness: claude appends a per-call "ARGUMENTS: <input>" trailer to the
// injected body. Two runs of the SAME skill version with DIFFERENT inputs must
// still share one coordinate (the trailer is stripped), while a genuine body
// edit must bucket apart even when the input is identical.
func TestContentHash_ClaudeStripsArgsTrailer(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())
	base := filepath.Join(t.TempDir(), ".claude", "skills", "code-review") // helper's skill name
	v1 := "\n# code-review\nLowercase; spaces to hyphens.\n"

	hashFor := func(skillBody, args string) string {
		rows := claudeSkillBodyRows(uuid.New(), base, skillBody+"\n\n\nARGUMENTS: "+args)
		d, _ := derive.DeriveSession(rows)
		derive.EnrichSkillIdentity(d.Spans, rows, nil)
		return contentHash(t, skillSpan(t, d.Spans))
	}

	a := hashFor(v1, "Hello, World!")
	b := hashFor(v1, "Qux (Test) Nine")
	if a == "" || a != b {
		t.Errorf("same version, different ARGUMENTS must share one coordinate: %q vs %q", a, b)
	}
	v2 := "\n# code-review\nLowercase; spaces to hyphens; strip punctuation.\n"
	if c := hashFor(v2, "Hello, World!"); c == a {
		t.Errorf("an edited body must bucket apart from the original even with identical input, both got %q", a)
	}
}

// TestContentHash_CodexPathLoadFromResult: a by-path skill load (codex reading
// SKILL.md) coordinates the run by the digest of the file body returned in the
// tool RESULT — the bytes the agent actually read, no disk access.
func TestContentHash_CodexPathLoadFromResult(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	gone := filepath.Join(t.TempDir(), ".codex", "skills", "code-review", "SKILL.md") // never created
	body := "---\nname: code-review\ndescription: read-by-path\n---\n# review\n"
	rows := codexPathLoadWithBody(uuid.New(), gone, body)
	d, _ := derive.DeriveSession(rows)
	derive.EnrichSkillIdentity(d.Spans, rows, nil)
	sp := skillSpan(t, d.Spans)

	if got := contentHash(t, sp); got == "" || got[:7] != "sha256:" {
		t.Fatalf("a by-path read body must coordinate the run; content_hash = %q", got)
	}
}

// TestContentHash_TranscriptPinnedPrefersSubtree: when the recorded load path is
// a store-worktree path (the sha is in the captured bytes), the coordinate is
// the proven full-subtree attestation — NOT a digest of the partial body the
// command happened to read.
func TestContentHash_TranscriptPinnedPrefersSubtree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	e := crEntry("94e539be7d6a01774d723a7c25513af0f070de7b")
	writeGlobalLock(t, home, e)
	skillMD := seedWorktree(t, e) // a real store-worktree path

	rows := codexPathLoadWithBody(uuid.New(), skillMD, "# a partial read of the body\n")
	d, _ := derive.DeriveSession(rows)
	derive.EnrichSkillIdentity(d.Spans, rows, nil)
	sp := skillSpan(t, d.Spans)

	if got := contentHash(t, sp); got != e.SubtreeHash {
		t.Errorf("a transcript-pinned load must coordinate by the proven subtree hash %q, got %q", e.SubtreeHash, got)
	}
}

// TestContentHash_NoBodyNoPin_Unset: a by-path load with no captured body and no
// store-path sha leaves content_hash unset — the honest unknown, never a
// disk-at-discover guess.
func TestContentHash_NoBodyNoPin_Unset(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	gone := filepath.Join(t.TempDir(), "skills", "code-review", "SKILL.md") // never created
	rows := codexSkillLoadRows(uuid.New(), gone)                            // function_call only, no result body
	d, _ := derive.DeriveSession(rows)
	derive.EnrichSkillIdentity(d.Spans, rows, nil)
	sp := skillSpan(t, d.Spans)

	if v, ok := sp.Attributes[derive.SkillContentHashKey]; ok {
		t.Errorf("no body and no sha-pin must leave content_hash unset; got %v", v)
	}
}

// TestContentHash_BodyDigestBeatsSnapshotSubtree is the priority guard: when a
// run captured its verbatim body, that run-immutable digest stays the coordinate
// even though the ingest snapshot also offers a subtree hash. The snapshot is
// frozen at FIRST INGEST, which can post-date a version switch, so it must never
// override the exact bytes the run loaded — otherwise a re-derive would re-bind
// an old run to the current version (the disk-at-discover bug).
func TestContentHash_BodyDigestBeatsSnapshotSubtree(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	base := filepath.Join(t.TempDir(), ".claude", "skills", "code-review") // symlink-shaped, no sha7
	body := "---\nname: code-review\n---\n# review\nthe body that actually ran\n"
	rows := claudeSkillBodyRows(uuid.New(), base, body)
	// The snapshot pins a DIFFERENT version's subtree (the post-switch install).
	snap := map[string]*model.LockEntry{"code-review": crEntry("94e539be7d6a01774d723a7c25513af0f070de7b")}

	d, _ := derive.DeriveSession(rows)
	derive.EnrichSkillIdentity(d.Spans, rows, snap)
	sp := skillSpan(t, d.Spans)

	got := contentHash(t, sp)
	if got == "" || got[:7] != "sha256:" {
		t.Fatalf("the loaded body must coordinate the run; got %q", got)
	}
	if got == snap["code-review"].SubtreeHash {
		t.Errorf("body digest must win over the snapshot subtree, but got the subtree %q", got)
	}
}

// TestContentHash_SnapshotSubtreeWhenNoBody: a load that captured NO body (a
// by-path read with no result) and is proven only by the ingest snapshot
// coordinates by the snapshot's frozen subtree hash — the last-resort
// run-immutable coordinate when there is no body digest to prefer.
func TestContentHash_SnapshotSubtreeWhenNoBody(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	gone := filepath.Join(t.TempDir(), ".codex", "skills", "code-review", "SKILL.md") // never created
	rows := codexSkillLoadRows(uuid.New(), gone)                                      // function_call only, no body
	e := crEntry("94e539be7d6a01774d723a7c25513af0f070de7b")
	snap := map[string]*model.LockEntry{"code-review": e}

	d, _ := derive.DeriveSession(rows)
	derive.EnrichSkillIdentity(d.Spans, rows, snap)
	sp := skillSpan(t, d.Spans)

	if got := contentHash(t, sp); got != e.SubtreeHash {
		t.Errorf("a no-body snapshot-proven load must coordinate by the frozen subtree %q, got %q", e.SubtreeHash, got)
	}
}

// TestContentHash_SnapshotAndTranscriptPinnedShareCohort: the SAME subtree proven
// two run-immutable ways — a transcript-pinned store load and a NO-BODY load
// proven by the ingest snapshot — lands in ONE cohort, because both coordinate by
// the proven subtree hash (neither has a competing body digest).
func TestContentHash_SnapshotAndTranscriptPinnedShareCohort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	e := crEntry("94e539be7d6a01774d723a7c25513af0f070de7b")
	writeGlobalLock(t, home, e)
	skillMD := seedWorktree(t, e) // a real store-worktree path

	// transcript-pinned store load → coordinate = the proven subtree hash.
	codexRows := codexPathLoadWithBody(uuid.New(), skillMD, "# a partial read\n")
	cd, _ := derive.DeriveSession(codexRows)
	derive.EnrichSkillIdentity(cd.Spans, codexRows, nil)
	pinnedHash := contentHash(t, skillSpan(t, cd.Spans))

	// no-body load proven only by the ingest snapshot of the SAME entry →
	// coordinate = the same frozen subtree hash.
	gone := filepath.Join(t.TempDir(), ".codex", "skills", "code-review", "SKILL.md")
	snapRows := codexSkillLoadRows(uuid.New(), gone)
	sd, _ := derive.DeriveSession(snapRows)
	derive.EnrichSkillIdentity(sd.Spans, snapRows, map[string]*model.LockEntry{"code-review": e})
	snapHash := contentHash(t, skillSpan(t, sd.Spans))

	if pinnedHash == "" || pinnedHash != snapHash {
		t.Errorf("same subtree proven two ways must share one cohort: pinned %q vs snapshot %q", pinnedHash, snapHash)
	}
	if pinnedHash != e.SubtreeHash {
		t.Errorf("the shared coordinate must be the proven subtree %q, got %q", e.SubtreeHash, pinnedHash)
	}
}

// TestActivation_ClaudeSkillTool_IsTool: a first-class Skill tool call is a
// genuine activation — skill.activation = "tool".
func TestActivation_ClaudeSkillTool_IsTool(t *testing.T) {
	rows := skillRows(uuid.New(), "", "") // claude Skill tool, no path evidence needed
	d, _ := derive.DeriveSession(rows)
	sp := skillSpan(t, d.Spans)
	if got := sp.Attributes[derive.SkillActivationKey]; got != derive.SkillActivationTool {
		t.Errorf("Skill tool call activation = %v, want %q", got, derive.SkillActivationTool)
	}
}

// TestActivation_CodexPathSignal_IsPath: a SKILL span lifted from a scraped
// /SKILL.md path in a command (codex reading a skill file) is a file-touch, not
// a first-class activation — skill.activation = "path".
func TestActivation_CodexPathSignal_IsPath(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())
	// Any resolvable …/skills/<name>/SKILL.md path triggers the path signal.
	dir := filepath.Join(t.TempDir(), "skills", "code-review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	skillMD := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("---\nname: code-review\ndescription: x\n---\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	rows := codexSkillLoadRows(uuid.New(), skillMD)
	d, _ := derive.DeriveSession(rows)
	sp := skillSpan(t, d.Spans)
	if got := sp.Attributes[derive.SkillActivationKey]; got != derive.SkillActivationPath {
		t.Errorf("path-signal SKILL span activation = %v, want %q", got, derive.SkillActivationPath)
	}
}
