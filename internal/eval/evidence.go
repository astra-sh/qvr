package eval

import (
	"encoding/json"
	"slices"
	"sort"

	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
)

// Evidence is the read model the graders judge: everything a deterministic
// grader needs about one captured session, distilled from its derived spans and
// session_meta. It deliberately carries no model output beyond the final
// assistant message — graders read behavior (tools, skills, outcome), not
// prose, except for the simple substring checks the text grader runs.
type Evidence struct {
	SessionID    string   `json:"session_id"`
	Skill        string   `json:"skill,omitempty"`
	Outcome      string   `json:"outcome,omitempty"` // session-level qvr.outcome rollup
	FinalMessage string   `json:"final_message,omitempty"`
	ToolSequence []string `json:"tool_sequence,omitempty"` // ordered tool names
	Skills       []string `json:"skills,omitempty"`        // skills that fired
	Tools        int      `json:"tools"`
	Turns        int      `json:"turns"`
	DurationMs   int64    `json:"duration_ms"`
}

// BuildEvidence distils a captured session into grader input. spans need not be
// pre-sorted; the tool sequence is ordered by span start time so an
// out-of-order store read can't scramble it.
func BuildEvidence(meta *store.SessionMetaRow, spans []*store.SpanRow) *Evidence {
	e := &Evidence{
		SessionID:  meta.SessionID.String(),
		Outcome:    meta.Outcome,
		Skills:     append([]string(nil), meta.Skills...),
		Turns:      int(meta.Turns),
		DurationMs: meta.DurationMs(),
	}

	ordered := append([]*store.SpanRow(nil), spans...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StartMs < ordered[j].StartMs })

	var lastLLMText string
	for _, sp := range ordered {
		attrs := decodeAttrs(sp.Attributes)
		switch sp.Kind {
		case derive.KindTool, derive.KindSkill:
			if name, _ := attrs["gen_ai.tool.name"].(string); name != "" {
				e.ToolSequence = append(e.ToolSequence, name)
			}
		case derive.KindLLM:
			if msg := lastAssistantText(attrs); msg != "" {
				lastLLMText = msg
			}
		}
	}
	e.FinalMessage = lastLLMText
	// Tools counts the same tool-named spans the tool_constraint grader walks
	// (ToolSequence, which includes the skill-load call), so a `maxTools` ceiling
	// means the same thing whether an author writes it under a behavior or a
	// tool_constraint grader. (Distinct from meta.Tools, which excludes SKILL
	// spans and feeds the session list, not the gate.)
	e.Tools = len(e.ToolSequence)
	return e
}

// decodeAttrs parses a span's JSON attribute blob, or returns an empty map.
func decodeAttrs(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}

// lastAssistantText pulls the assistant message text out of an LLM span's
// gen_ai.output.messages attribute (a JSON-encoded [{role,content}] array).
func lastAssistantText(attrs map[string]any) string {
	raw, _ := attrs["gen_ai.output.messages"].(string)
	if raw == "" {
		return ""
	}
	var msgs []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		return ""
	}
	for _, m := range slices.Backward(msgs) {
		if m.Role == "assistant" && m.Content != "" {
			return m.Content
		}
	}
	return ""
}
