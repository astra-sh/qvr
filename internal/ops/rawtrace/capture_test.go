package rawtrace_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
	"github.com/raks097/quiver/internal/ops/rawtrace"
	"github.com/raks097/quiver/internal/ops/store"
)

// newStore opens a throwaway SQLite store under t.TempDir().
func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), store.OpenOptions{
		Path: filepath.Join(t.TempDir(), "ops.db"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func payloadJSON(t *testing.T, sessionID, transcript string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"session_id":      sessionID,
		"transcript_path": transcript,
		"cwd":             "/tmp/proj",
		"hook_event_name": "PostToolUse",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCapture_TailsTranscriptVerbatim is the core guarantee: native transcript
// lines are stored byte-for-byte, the hook payload is stored too, and a second
// firing only picks up newly appended lines (cursor-based idempotent tailing).
func TestCapture_TailsTranscriptVerbatim(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	sessionID := "550e8400-e29b-41d4-a716-446655440000"
	transcript := filepath.Join(t.TempDir(), "session.jsonl")

	// Two native lines, deliberately with nested structure + a thinking block
	// so we can prove nothing is flattened or truncated.
	line1 := `{"type":"user","message":{"role":"user","content":"fix the bug"}}`
	line2 := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"let me reason about this carefully"},{"type":"tool_use","name":"Edit"}]}}`
	if err := os.WriteFile(transcript, []byte(line1+"\n"+line2+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := rawtrace.Capture(ctx, s, "claude-code", "PostToolUse", payloadJSON(t, sessionID, transcript))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if res.LinesStored != 2 {
		t.Fatalf("first capture: want 2 transcript lines, got %d", res.LinesStored)
	}
	if !res.HookStored {
		t.Fatal("first capture: expected hook payload stored")
	}

	sid := res.SessionID
	rows := queryRows(t, s, sid)
	// 2 transcript + 1 hook_payload, in capture order.
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if got := string(rows[0].Raw); got != line1 {
		t.Errorf("row0 not verbatim:\n got: %s\nwant: %s", got, line1)
	}
	if got := string(rows[1].Raw); got != line2 {
		t.Errorf("row1 not verbatim:\n got: %s\nwant: %s", got, line2)
	}
	if rows[2].Source != ops.RawSourceHookPayload || rows[2].HookType != "PostToolUse" {
		t.Errorf("row2 want hook_payload/PostToolUse, got %s/%s", rows[2].Source, rows[2].HookType)
	}
	// seq is dense and monotonic.
	for i, r := range rows {
		if r.Seq != i {
			t.Errorf("row %d: want seq %d, got %d", i, i, r.Seq)
		}
	}

	// Append a third line; a second firing must store ONLY the new line.
	line3 := `{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line3 + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	res2, err := rawtrace.Capture(ctx, s, "claude-code", "Stop", payloadJSON(t, sessionID, transcript))
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if res2.LinesStored != 1 {
		t.Fatalf("second capture: want 1 new transcript line, got %d", res2.LinesStored)
	}

	rows = queryRows(t, s, sid)
	// 3 transcript + 2 hook_payload.
	if len(rows) != 5 {
		t.Fatalf("after second capture: want 5 rows, got %d", len(rows))
	}
	// The newly tailed transcript line must be verbatim and correctly ordered
	// before the second hook payload.
	var gotLine3 bool
	for _, r := range rows {
		if r.Source == ops.RawSourceTranscript && string(r.Raw) == line3 {
			gotLine3 = true
		}
	}
	if !gotLine3 {
		t.Error("third transcript line not captured verbatim on second firing")
	}
}

// TestCapture_NoTranscript_StillStoresHookPayload proves a firing with no
// locatable transcript still preserves the raw hook payload.
func TestCapture_NoTranscript_StillStoresHookPayload(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	payload := payloadJSON(t, "11111111-1111-1111-1111-111111111111", "/does/not/exist.jsonl")
	res, err := rawtrace.Capture(ctx, s, "codex", "PreToolUse", payload)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if res.LinesStored != 0 {
		t.Errorf("want 0 transcript lines, got %d", res.LinesStored)
	}
	if !res.HookStored {
		t.Fatal("expected hook payload stored")
	}
	rows := queryRows(t, s, res.SessionID)
	if len(rows) != 1 || rows[0].Source != ops.RawSourceHookPayload {
		t.Fatalf("want 1 hook_payload row, got %d rows", len(rows))
	}
	if string(rows[0].Raw) != string(payload) {
		t.Error("hook payload not stored verbatim")
	}
}

func queryRows(t *testing.T, s store.Store, sid uuid.UUID) []*ops.RawTrace {
	t.Helper()
	rows, err := s.QueryRawTraces(context.Background(), &store.RawTraceFilter{SessionID: &sid})
	if err != nil {
		t.Fatalf("query raw traces: %v", err)
	}
	return rows
}
