package derive_test

import (
	"testing"

	"github.com/astra-sh/qvr/internal/config"
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
//   - SessionMeta.Outcome rolls up to failure only when the failed fraction of
//     outcome-bearing spans exceeds the threshold (default >80%); else blocked
//     (if any) or success.
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

// TestOutcome_SkillLoadSuccess pins the v9 signal: a skill invoked via the Skill
// tool with no other tool calls — its load confirmed by the base-directory
// injection — carries qvr.outcome=success and rolls up to success, not unknown.
// The flagship advisory/triage eval leads with `outcome: success`; before v9 a
// tool-less skill could never pass it.
func TestOutcome_SkillLoadSuccess(t *testing.T) {
	d, err := derive.DeriveSession(agentRows("claude",
		`{"type":"user","timestamp":"2026-06-12T00:00:00.000Z","message":{"role":"user","content":"triage this"}}`,
		`{"type":"assistant","timestamp":"2026-06-12T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"sk1","name":"Skill","input":{"skill":"triage"}}]}}`,
		`{"type":"user","timestamp":"2026-06-12T00:00:02.000Z","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /p/.claude/skills/triage\n# body"}]}}`,
	))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	sp := findSkillSpan(t, d)
	if got := sp.Attributes[derive.OutcomeKey]; got != derive.OutcomeSuccess {
		t.Errorf("SKILL span outcome = %v, want success for a confirmed load", got)
	}
	if d.Meta.Tools != 0 {
		t.Errorf("tools = %d, want 0 (the load is a SKILL span, not a tool call)", d.Meta.Tools)
	}
	if d.Meta.Outcome != derive.OutcomeSuccess {
		t.Errorf("meta outcome = %q, want %q for a tool-less skill that loaded",
			d.Meta.Outcome, derive.OutcomeSuccess)
	}
}

// twoToolRun builds a claude session whose single assistant turn fires two Bash
// tools, with tu1's and tu2's results carrying the given error flags. Lets a
// test set the failed fraction of a session precisely.
func twoToolRun(sid uuid.UUID, tu1Err, tu2Err bool) []*ops.RawTrace {
	flag := func(b bool) string {
		if b {
			return `,"is_error":true`
		}
		return ""
	}
	return []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-12T00:00:00.000Z","message":{"role":"user","content":"run both"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-12T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ok"}},{"type":"tool_use","id":"tu2","name":"Bash","input":{"command":"bad"}}]}}`),
		row(sid, 2, `{"type":"user","timestamp":"2026-06-12T00:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"fine"`+flag(tu1Err)+`}]}}`),
		row(sid, 3, `{"type":"user","timestamp":"2026-06-12T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":"boom"`+flag(tu2Err)+`}]}}`),
	}
}

// TestOutcome_SessionFailureThreshold pins the rollup policy: a session is
// failure only when the FAILED fraction of its outcome-bearing spans exceeds
// the threshold (default >80%). A minority of errored tool calls amid an
// otherwise successful task rolls up to success, not failure — the old
// worst-of-spans rule let a single error doom the whole session.
func TestOutcome_SessionFailureThreshold(t *testing.T) {
	// 1 of 2 failed (50%) — below the 80% default, so NOT a failed session.
	mixed := uuid.MustParse("550e8400-e29b-41d4-a716-44665544aa05")
	dm, err := derive.DeriveSession(twoToolRun(mixed, false, true))
	if err != nil {
		t.Fatalf("derive mixed: %v", err)
	}
	if dm.Meta.Outcome != derive.OutcomeSuccess {
		t.Errorf("50%% failed: meta outcome = %q, want %q (minority failure doesn't doom the session)",
			dm.Meta.Outcome, derive.OutcomeSuccess)
	}

	// Both failed (100% > 80%) — a genuinely failed session.
	allBad := uuid.MustParse("550e8400-e29b-41d4-a716-44665544aa06")
	da, err := derive.DeriveSession(twoToolRun(allBad, true, true))
	if err != nil {
		t.Fatalf("derive allBad: %v", err)
	}
	if da.Meta.Outcome != derive.OutcomeFailure {
		t.Errorf("100%% failed: meta outcome = %q, want %q", da.Meta.Outcome, derive.OutcomeFailure)
	}
}

// TestOutcome_FailureThresholdConfigurable pins that ops.outcome_failure_threshold
// overrides the default: at 0.4 the same 50%-failed session that reads success
// under the default now rolls up to failure.
func TestOutcome_FailureThresholdConfigurable(t *testing.T) {
	// Restore the default for every later test, regardless of outcome.
	defer derive.ConfigureOutcome(&config.Config{Ops: config.OpsConfig{OutcomeFailureThreshold: derive.DefaultOutcomeFailureThreshold}})
	derive.ConfigureOutcome(&config.Config{Ops: config.OpsConfig{OutcomeFailureThreshold: 0.4}})

	sid := uuid.MustParse("550e8400-e29b-41d4-a716-44665544aa07")
	d, err := derive.DeriveSession(twoToolRun(sid, false, true))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.Meta.Outcome != derive.OutcomeFailure {
		t.Errorf("50%% failed at threshold 0.4: meta outcome = %q, want %q", d.Meta.Outcome, derive.OutcomeFailure)
	}
}

// codexExecRun builds a minimal codex session: a prompt, an exec function_call,
// and its function_call_output carrying the given output envelope verbatim.
func codexExecRun(sid uuid.UUID, output string) []*ops.RawTrace {
	return []*ops.RawTrace{
		codexRow(sid, 0, `{"timestamp":"2026-06-12T00:00:00.000Z","type":"event_msg","payload":{"type":"user_message","message":"run it"}}`),
		codexRow(sid, 1, `{"timestamp":"2026-06-12T00:00:01.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"build\"}","call_id":"c1"}}`),
		codexRow(sid, 2, `{"timestamp":"2026-06-12T00:00:02.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":`+output+`}}`),
	}
}

// TestOutcome_CodexExitCode pins codex outcome derivation: codex folds the
// outcome into the output payload (no separate error flag), so a non-zero
// exit_code must derive to failure — otherwise every codex tool/skill result
// reads as success and the observed outcome is wrong on codex.
func TestOutcome_CodexExitCode(t *testing.T) {
	// A non-zero exit_code, double-encoded as a JSON string (codex's exec shape).
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-44665544bb01")
	d, err := derive.DeriveSession(codexExecRun(sid, jsonQuote(`{"output":"build failed","metadata":{"exit_code":2}}`)))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.Meta.Outcome != derive.OutcomeFailure {
		t.Errorf("non-zero exit_code: meta outcome = %q, want %q", d.Meta.Outcome, derive.OutcomeFailure)
	}

	// exit_code 0 → success.
	sid = uuid.MustParse("550e8400-e29b-41d4-a716-44665544bb02")
	d, err = derive.DeriveSession(codexExecRun(sid, jsonQuote(`{"output":"ok","metadata":{"exit_code":0}}`)))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.Meta.Outcome != derive.OutcomeSuccess {
		t.Errorf("exit_code 0: meta outcome = %q, want %q", d.Meta.Outcome, derive.OutcomeSuccess)
	}

	// A plain-string output (no structured envelope) carries no failure signal.
	sid = uuid.MustParse("550e8400-e29b-41d4-a716-44665544bb03")
	d, err = derive.DeriveSession(codexExecRun(sid, jsonQuote("AGENTS.md\nCLAUDE.md")))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.Meta.Outcome != derive.OutcomeSuccess {
		t.Errorf("plain output: meta outcome = %q, want %q", d.Meta.Outcome, derive.OutcomeSuccess)
	}
}

// TestOutcome_BlockedWithoutErrorFlag pins fix #12: a user interrupt / rejection
// is a governance outcome echoed in the result text, and some harnesses surface
// it WITHOUT setting is_error (observed on skill loads). It must classify as
// blocked, not success.
func TestOutcome_BlockedWithoutErrorFlag(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-44665544bb04")
	d, err := derive.DeriveSession(claudeToolRun(sid,
		"[Request interrupted by user]", false)) // note: is_error NOT set
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	_, tool, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, tool, map[string]any{derive.OutcomeKey: derive.OutcomeBlocked})
	if d.Meta.Outcome != derive.OutcomeBlocked {
		t.Errorf("meta outcome = %q, want %q for an interrupt without the error flag", d.Meta.Outcome, derive.OutcomeBlocked)
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
