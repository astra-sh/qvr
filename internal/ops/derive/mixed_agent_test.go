package derive_test

import (
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// TestDeriveSession_MixedAgentRows pins the dominant-agent dispatch guard: a
// stale external writer can inject rows under another agent name into the
// same session id, and dispatching on the first row would run that agent's
// deriver against the real agent's record shapes — deriving the session to
// zero spans. The majority transcript agent must win even when the foreign
// rows come FIRST, and the foreign rows must not pollute the derivation.
func TestDeriveSession_MixedAgentRows(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440003")
	mk := func(agent, source string, seq int, raw string) *ops.RawTrace {
		return &ops.RawTrace{
			AgentName: agent, SessionID: sid, Source: source, Seq: seq,
			CapturedAt: time.Now(), Raw: []byte(raw),
		}
	}

	rows := []*ops.RawTrace{
		// The foreign writer's rows land first (lower seq): a hook payload and
		// one transcript row in its own format.
		mk("claude-code", "hook_payload", 0, `{"hook_event_name":"PreToolUse"}`),
		mk("claude-code", ops.RawSourceTranscript, 1, `{"type":"user","timestamp":"2026-06-12T10:00:00.000Z","message":{"role":"user","content":"injected"}}`),
		// The session's real agent: a copilot transcript with a prompt, a
		// skill-tool call, and a reply — three rows, the majority.
		mk("copilot", ops.RawSourceTranscript, 2, `{"type":"user.message","timestamp":"2026-06-12T10:00:01.000Z","data":{"content":"verify the plumbing"}}`),
		mk("copilot", ops.RawSourceTranscript, 3, `{"type":"assistant.message","timestamp":"2026-06-12T10:00:02.000Z","data":{"content":"on it","toolRequests":[{"toolCallId":"t1","name":"skill","arguments":{"name":"qvr-probe"}}]}}`),
		mk("copilot", ops.RawSourceTranscript, 4, `{"type":"assistant.message","timestamp":"2026-06-12T10:00:03.000Z","data":{"content":"PROBE OK"}}`),
	}

	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.Meta.Agent != "copilot" {
		t.Fatalf("meta agent = %q, want copilot (the majority transcript agent)", d.Meta.Agent)
	}
	llm, _, skill := splitSpanKinds(d.Spans)
	if llm == nil || skill == nil {
		t.Fatalf("want LLM+SKILL spans from the copilot rows, got %d spans", len(d.Spans))
	}
	if name := skill.Attributes["skill.name"]; name != "qvr-probe" {
		t.Errorf("skill.name = %v, want qvr-probe", name)
	}
	if d.Meta.Title != "verify the plumbing" {
		t.Errorf("title = %q — the injected foreign prompt must not become the session title", d.Meta.Title)
	}
}

// TestDeriveSession_AliasRowsAreNotForeign pins the canonical-name collapse:
// rows recorded under a legacy alias of the SAME agent (claude-code vs
// claude) are not a mixed-agent session — nothing is filtered.
func TestDeriveSession_AliasRowsAreNotForeign(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440004")
	mk := func(agent string, seq int, raw string) *ops.RawTrace {
		return &ops.RawTrace{
			AgentName: agent, SessionID: sid, Source: ops.RawSourceTranscript,
			Seq: seq, CapturedAt: time.Now(), Raw: []byte(raw),
		}
	}
	rows := []*ops.RawTrace{
		mk("claude-code", 0, `{"type":"user","timestamp":"2026-06-12T10:00:00.000Z","message":{"role":"user","content":"hi"}}`),
		mk("claude", 1, `{"type":"assistant","timestamp":"2026-06-12T10:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hello"}]}}`),
	}
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.Meta.Agent != "claude" {
		t.Errorf("meta agent = %q, want claude", d.Meta.Agent)
	}
	if d.Meta.Turns != 1 || d.Meta.Title != "hi" {
		t.Errorf("alias rows were filtered: turns=%d title=%q", d.Meta.Turns, d.Meta.Title)
	}
}
