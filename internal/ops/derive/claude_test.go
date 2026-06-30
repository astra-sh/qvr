package derive_test

import (
	"strings"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// row builds a transcript RawTrace from a verbatim JSONL line.
func row(sid uuid.UUID, seq int, raw string) *ops.RawTrace {
	return &ops.RawTrace{
		AgentName:  "claude-code",
		SessionID:  sid,
		Source:     ops.RawSourceTranscript,
		Seq:        seq,
		CapturedAt: time.Now(),
		Raw:        []byte(raw),
	}
}

func TestClaudeDerive_TurnToolSkill(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"add a feature"}}`),
		// assistant: thinking + a Skill load + a Read tool call, with usage+model.
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":20},"content":[`+
			`{"type":"thinking","thinking":"plan"},`+
			`{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{"command":"code-review"}},`+
			`{"type":"tool_use","id":"toolu_read","name":"Read","input":{"file_path":"/x/main.go"}}]}}`),
		// tool_result for the Read.
		row(sid, 2, `{"type":"user","timestamp":"2026-06-02T00:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_read","content":"package main","is_error":false}]}}`),
		// final assistant text.
		row(sid, 3, `{"type":"assistant","timestamp":"2026-06-02T00:00:03.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"output_tokens":5},"content":[{"type":"text","text":"done"}]}}`),
	}

	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	spans := d.Spans

	llm, tool, skill := splitSpanKinds(spans)
	if llm == nil || tool == nil || skill == nil {
		t.Fatalf("want LLM+TOOL+SKILL spans, got %d spans (%+v)", len(spans), spans)
	}

	// LLM turn: OTel gen_ai chat conventions — tokens, model, messages.
	wantAttrs(t, llm, map[string]any{
		"gen_ai.usage.input_tokens":  100,
		"gen_ai.usage.output_tokens": 25,
		"gen_ai.request.model":       "claude-opus-4-8",
		"gen_ai.provider.name":       "anthropic",
	})
	if s, _ := llm.Attributes["gen_ai.input.messages"].(string); !strings.Contains(s, "add a feature") {
		t.Errorf("input messages missing prompt: got %v", llm.Attributes["gen_ai.input.messages"])
	}
	if s, _ := llm.Attributes["gen_ai.output.messages"].(string); !strings.Contains(s, "done") {
		t.Errorf("output messages missing text: got %v", llm.Attributes["gen_ai.output.messages"])
	}

	// SKILL span: OTel execute_tool + the Quiver skill.name extension tag.
	wantAttrs(t, skill, map[string]any{
		"skill.name":            "code-review",
		"gen_ai.operation.name": "execute_tool",
	})
	if skill.ParentSpanID != llm.SpanID {
		t.Errorf("skill should parent to the turn LLM span")
	}

	// TOOL span: gen_ai.tool.* with result attached, parents to the turn.
	wantAttrs(t, tool, map[string]any{
		"gen_ai.tool.name":        "Read",
		"gen_ai.tool.call.result": "package main",
	})
	if tool.ParentSpanID != llm.SpanID {
		t.Errorf("tool should parent to the turn LLM span")
	}

	// Determinism: re-derivation reproduces identical ids.
	again, _ := derive.DeriveSession(rows)
	if again.Spans[0].SpanID != spans[0].SpanID || again.Spans[0].TraceID != spans[0].TraceID {
		t.Error("derivation is not deterministic")
	}

	// Unified session meta: constructed from the same walk.
	wantMeta(t, d.Meta, "claude", "add a feature", "claude-opus-4-8", 1, 1, "code-review")
}

// TestClaudeDerive_SkillPathIORemainsTool pins Bug A: on claude the first-class
// Skill tool is the only load signal, so ordinary Bash/Read/Edit calls that
// merely touch a skill's source files — catting its SKILL.md, git-adding its
// dir — must stay plain TOOL spans and carry NO skill attribution. Otherwise
// authoring I/O inflates a skill's per-version activity with calls that never
// used it.
func TestClaudeDerive_SkillPathIORemainsTool(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440009")
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-24T00:00:00.000Z","message":{"role":"user","content":"edit the skill"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-24T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[`+
			`{"type":"tool_use","id":"t_cat","name":"Bash","input":{"command":"cat skills/evolve-skill/SKILL.md"}},`+
			`{"type":"tool_use","id":"t_edit","name":"Edit","input":{"file_path":"skills/evolve-skill/SKILL.md"}},`+
			`{"type":"tool_use","id":"t_mk","name":"Bash","input":{"command":"mkdir -p $REG/skills/clean-skill"}}]}}`),
	}

	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	for i := range d.Spans {
		sp := &d.Spans[i]
		if sp.Kind == derive.KindSkill {
			t.Errorf("skill-path I/O lifted to a SKILL span: %+v", sp.Attributes)
		}
		if name, ok := sp.Attributes["skill.name"]; ok {
			t.Errorf("%s carries skill.name=%v — authoring I/O must not be attributed", sp.Name, name)
		}
	}
	// And no phantom skill leaks into the session rollup.
	if len(d.Meta.Skills) != 0 {
		t.Errorf("meta skills = %v, want none (no Skill tool was invoked)", d.Meta.Skills)
	}
}

// wantMeta asserts the unified session meta's core fields plus valid time
// bounds and a single-skill list.
func wantMeta(t *testing.T, m derive.SessionMeta, agent, title, model string, turns, tools int64, skill string) {
	t.Helper()
	if m.Agent != agent {
		t.Errorf("meta agent = %q, want %q", m.Agent, agent)
	}
	if m.Title != title {
		t.Errorf("meta title = %q, want %q", m.Title, title)
	}
	if m.Model != model {
		t.Errorf("meta model = %q, want %q", m.Model, model)
	}
	if m.Turns != turns || m.Tools != tools {
		t.Errorf("meta counts = %d turns / %d tools, want %d/%d", m.Turns, m.Tools, turns, tools)
	}
	if len(m.Skills) != 1 || m.Skills[0] != skill {
		t.Errorf("meta skills = %v, want [%s]", m.Skills, skill)
	}
	if m.StartedMs == 0 || m.EndedMs < m.StartedMs {
		t.Errorf("meta time bounds invalid: %d..%d", m.StartedMs, m.EndedMs)
	}
}
