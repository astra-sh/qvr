package derive_test

import (
	"testing"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// Outcome contract (deriver v8):
//   - TOOL/SKILL spans carry qvr.outcome ONLY once their result arrives —
//     success when the result is not an error, failure when it is, and blocked
//     when the error text is a user denial/interrupt (a governance signal).
//   - A call whose result is never seen carries NO outcome (absence = unknown);
//     we never fabricate a success.
//   - SessionMeta.Outcome is the worst-of-spans rollup.
//   - The OTLP span status code reflects the outcome (ERROR for failure/blocked)
//     instead of the old hardcoded OK.

// claudeToolRun builds a minimal claude session: a prompt, a Bash tool_use, and
// its tool_result with the given text + error flag.
func claudeToolRun(sid uuid.UUID, result string, isError bool) []*ops.RawTrace {
	errField := ""
	if isError {
		errField = `,"is_error":true`
	}
	return []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-12T00:00:00.000Z","message":{"role":"user","content":"run it"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-12T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls"}}]}}`),
		row(sid, 2, `{"type":"user","timestamp":"2026-06-12T00:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":`+jsonQuote(result)+errField+`}]}}`),
	}
}

// jsonQuote returns a JSON string literal for s.
func jsonQuote(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"', '\\':
			out = append(out, '\\', byte(r))
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, []byte(string(r))...)
		}
	}
	return string(append(out, '"'))
}

func TestOutcome_Success(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-44665544aa01")
	d, err := derive.DeriveSession(claudeToolRun(sid, "done", false))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	_, tool, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, tool, map[string]any{derive.OutcomeKey: derive.OutcomeSuccess})
	if d.Meta.Outcome != derive.OutcomeSuccess {
		t.Errorf("meta outcome = %q, want %q", d.Meta.Outcome, derive.OutcomeSuccess)
	}
}

func TestOutcome_Failure(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-44665544aa02")
	d, err := derive.DeriveSession(claudeToolRun(sid, "command not found", true))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	_, tool, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, tool, map[string]any{derive.OutcomeKey: derive.OutcomeFailure})
	if d.Meta.Outcome != derive.OutcomeFailure {
		t.Errorf("meta outcome = %q, want %q", d.Meta.Outcome, derive.OutcomeFailure)
	}
	// The OTLP status code must now be ERROR(2), not the old hardcoded OK(1).
	if code := otlpStatusForSpan(derive.ToOTLP(d.Spans), tool.SpanID); code != 2 {
		t.Errorf("OTLP status code = %v, want 2 (ERROR) for a failed tool call", code)
	}
}

func TestOutcome_Blocked(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-44665544aa03")
	// A user-rejected tool call: is_error with the harness denial text.
	d, err := derive.DeriveSession(claudeToolRun(sid,
		"The user doesn't want to proceed with this tool use. The tool use was rejected.", true))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	_, tool, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, tool, map[string]any{derive.OutcomeKey: derive.OutcomeBlocked})
	if d.Meta.Outcome != derive.OutcomeBlocked {
		t.Errorf("meta outcome = %q, want %q", d.Meta.Outcome, derive.OutcomeBlocked)
	}
}

// TestOutcome_PendingIsUnknown pins the absence invariant: a tool call whose
// result never arrives carries no outcome, and the session rolls up to "".
func TestOutcome_PendingIsUnknown(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-44665544aa04")
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-12T00:00:00.000Z","message":{"role":"user","content":"run it"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-12T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls"}}]}}`),
	}
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	_, tool, _ := splitSpanKinds(d.Spans)
	if v, ok := tool.Attributes[derive.OutcomeKey]; ok {
		t.Errorf("qvr.outcome present (%v) on a call with no result — fabricated outcome", v)
	}
	if d.Meta.Outcome != "" {
		t.Errorf("meta outcome = %q, want \"\" (unknown) when no span carried an outcome", d.Meta.Outcome)
	}
}

// TestOutcome_SessionWorstOf pins the rollup: one success and one failure in the
// same session roll up to failure (the worst).
func TestOutcome_SessionWorstOf(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-44665544aa05")
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-12T00:00:00.000Z","message":{"role":"user","content":"run both"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-12T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ok"}},{"type":"tool_use","id":"tu2","name":"Bash","input":{"command":"bad"}}]}}`),
		row(sid, 2, `{"type":"user","timestamp":"2026-06-12T00:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"fine"}]}}`),
		row(sid, 3, `{"type":"user","timestamp":"2026-06-12T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":"boom","is_error":true}]}}`),
	}
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.Meta.Outcome != derive.OutcomeFailure {
		t.Errorf("meta outcome = %q, want %q (worst-of success+failure)", d.Meta.Outcome, derive.OutcomeFailure)
	}
}

// otlpStatusForSpan digs the status.code out of a ToOTLP envelope for one span,
// or -1 when not found.
func otlpStatusForSpan(env map[string]any, spanID string) int {
	rs, _ := env["resourceSpans"].([]map[string]any)
	for _, r := range rs {
		ss, _ := r["scopeSpans"].([]map[string]any)
		for _, sc := range ss {
			spans, _ := sc["spans"].([]map[string]any)
			for _, sp := range spans {
				if sp["spanId"] == spanID {
					if st, ok := sp["status"].(map[string]any); ok {
						if c, ok := st["code"].(int); ok {
							return c
						}
					}
				}
			}
		}
	}
	return -1
}
