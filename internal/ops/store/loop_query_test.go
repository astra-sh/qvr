package store

import (
	"context"
	"testing"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"
)

// seedLoopFixture lays down one skill ("triage") across two content versions,
// two commit-only versions (the skill dir was gone at ingest so no content hash
// was observed, but a proven commit sha survives in the recorded path), plus a
// truly-uncoordinated span, with mixed run statuses and activation provenance —
// the shape the evolution loop reads.
//
//	v1 (content_hash sha256:aaaa…): 2 tool-activations, 1 success + 1 failure
//	v2 (content_hash sha256:bbbb…): 1 tool-activation, success
//	path-touch (v1 hash, activation=path): 1 span — must be excluded by --activation tool
//	c1 (commit cccc111, no content_hash): 1 tool-activation, success — coalesces to the commit
//	c2 (commit dddd222, no content_hash): 1 tool-activation, failure — a distinct commit cohort
//	unknown (no content_hash, no commit): 1 tool-activation, blocked
func seedLoopFixture(t *testing.T, st Store) uuid.UUID {
	t.Helper()
	sid := uuid.New()
	skill := func(id, chash, commit, ref, activation, outcome string, ms int64) *SpanRow {
		attrs := `{"skill.name":"triage"`
		if chash != "" {
			attrs += `,"skill.content_hash":"` + chash + `"`
		}
		if commit != "" {
			attrs += `,"skill.commit":"` + commit + `"`
		}
		if ref != "" {
			attrs += `,"skill.version":"` + ref + `"`
		}
		if activation != "" {
			attrs += `,"skill.activation":"` + activation + `"`
		}
		if outcome != "" {
			attrs += `,"qvr.outcome":"` + outcome + `"`
		}
		attrs += `}`
		return &SpanRow{SpanID: id, TraceID: "tr", SessionID: sid, AgentName: "codex",
			Kind: "SKILL", Name: id, StartMs: ms, EndMs: ms, Attributes: attrs}
	}
	rows := []*SpanRow{
		skill("v1a", "sha256:aaaa1111", "", "main", "tool", "success", 100),
		skill("v1b", "sha256:aaaa1111", "", "main", "tool", "failure", 110),
		skill("v2a", "sha256:bbbb2222", "", "main", "tool", "success", 200),
		skill("path", "sha256:aaaa1111", "", "main", "path", "success", 120),
		skill("c1", "", "cccc111", "cccc111", "tool", "success", 300),
		skill("c2", "", "dddd222", "dddd222", "tool", "failure", 310),
		skill("unk", "", "", "", "tool", "blocked", 130),
	}
	if err := st.ReplaceSessionSpans(context.Background(), sid, rows); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return sid
}

func TestQuerySpans_LoopFilters(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seedLoopFixture(t, st)

	count := func(f *SpanFilter) int {
		t.Helper()
		got, err := st.QuerySpans(ctx, f)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		return len(got)
	}

	if n := count(&SpanFilter{Skills: []string{"triage"}}); n != 7 {
		t.Errorf("--skill triage: want 7 spans, got %d", n)
	}
	// Version is a content-hash PREFIX (git-style short hash) over the hex.
	if n := count(&SpanFilter{Versions: []string{"aaaa"}}); n != 3 {
		t.Errorf("--version aaaa (prefix): want 3 spans, got %d", n)
	}
	if n := count(&SpanFilter{Statuses: []string{"failure"}}); n != 2 {
		t.Errorf("--status failure: want 2 spans, got %d", n)
	}
	if n := count(&SpanFilter{Activations: []string{"tool"}}); n != 6 {
		t.Errorf("--activation tool: want 6 spans, got %d", n)
	}
	// Combined: tool-activations of v1 that failed.
	if n := count(&SpanFilter{Versions: []string{"aaaa"}, Activations: []string{"tool"}, Statuses: []string{"failure"}}); n != 1 {
		t.Errorf("combined v1+tool+failure: want 1 span, got %d", n)
	}
}

func TestSkillContentRollup(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seedLoopFixture(t, st)

	if _, err := st.SkillContentRollup(ctx, &MetricsFilter{}); err == nil {
		t.Error("SkillContentRollup must require a Skill")
	}

	// Scope to genuine activations: the path-touch span drops out.
	got, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "triage", Activation: "tool"})
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	by := map[string]*SkillContentCohort{}
	for _, c := range got {
		by[c.ContentHash] = c
	}
	// want asserts one cohort's activation count and success/failure/blocked mix.
	want := func(coord string, acts, succ, fail, blk int64) {
		t.Helper()
		c := by[coord]
		if c == nil {
			t.Errorf("cohort %q missing", coord)
			return
		}
		if c.Activations != acts || c.Success != succ || c.Failure != fail || c.Blocked != blk {
			t.Errorf("cohort %q = %+v, want acts=%d succ=%d fail=%d blk=%d", coord, c, acts, succ, fail, blk)
		}
	}
	// v1, v2 (content hash) + c1, c2 (commit-only, recovered by the coalesced
	// coordinate) + the true unknown = 5 cohorts.
	if len(by) != 5 {
		t.Fatalf("want 5 cohorts (v1, v2, c1, c2, unknown), got %d: %+v", len(by), got)
	}
	want("sha256:aaaa1111", 2, 1, 1, 0) // path-touch excluded by --activation tool
	// A run whose skill dir was gone at ingest (no content_hash) still cohorts by
	// its proven commit — the durable version tag survives uninstall — rather
	// than collapsing into unknown. c1 and c2 are distinct commits ⇒ distinct
	// cohorts (the before/after the loop needs).
	want("cccc111", 1, 1, 0, 0)
	want("dddd222", 1, 0, 1, 0)
	// Only the span with neither content hash nor commit is the true unknown.
	want("", 1, 0, 0, 1)
	// Newest-first by first-fired: c2 (ms 310) leads.
	if got[0].ContentHash != "dddd222" {
		t.Errorf("cohorts should be newest-first; got first = %q", got[0].ContentHash)
	}
}
