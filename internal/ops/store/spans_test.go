package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"
)

func openTestStore(t *testing.T) Store {
	t.Helper()
	st, err := Open(context.Background(), OpenOptions{Path: filepath.Join(t.TempDir(), "skillops.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestReplaceSessionSpans_ToleratesDuplicateID pins the #147 backstop: a
// duplicate span_id in the row set must not abort the whole insert (which would
// lose every span for the session). The colliding row is dropped (last wins)
// and the rest persist.
func TestReplaceSessionSpans_ToleratesDuplicateID(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	rows := []*SpanRow{
		{SpanID: "dup", TraceID: "t", SessionID: sid, AgentName: "codex", Kind: "TOOL", Name: "begin", StartMs: 1, EndMs: 1},
		{SpanID: "dup", TraceID: "t", SessionID: sid, AgentName: "codex", Kind: "TOOL", Name: "end", StartMs: 1, EndMs: 5},
		{SpanID: "uniq", TraceID: "t", SessionID: sid, AgentName: "codex", Kind: "LLM", Name: "turn", StartMs: 0, EndMs: 6},
	}
	if err := st.ReplaceSessionSpans(ctx, sid, rows); err != nil {
		t.Fatalf("ReplaceSessionSpans must not fail on a duplicate span_id: %v", err)
	}

	got, err := st.QuerySpans(ctx, &SpanFilter{SessionID: &sid})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 persisted spans (dup collapsed), got %d", len(got))
	}
	// Last write wins for the collapsed id: the completion ("end", EndMs=5).
	for _, sp := range got {
		if sp.SpanID == "dup" && sp.EndMs != 5 {
			t.Errorf("collapsed span should keep last row (EndMs=5), got %d", sp.EndMs)
		}
	}
}
