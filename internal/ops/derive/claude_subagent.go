package derive

import (
	"bytes"
	"encoding/json"

	"github.com/astra-sh/qvr/internal/ops"
)

// Subagent nesting for the claude deriver. Claude Code spawns a subagent via the
// Agent (or legacy Task) tool and records that subagent's whole run in a sibling
// file, subagents/agent-<agentId>.jsonl, whose lines carry the PARENT session's
// sessionId — so they ingest under the same session. Every subagent line is
// tagged with its agentId; the main agent's lines carry none. We split on that,
// derive each subagent as its own subtree, and hang it under the Agent tool span
// that spawned it, so the trace reads as one nested tree (Agent → subagent →
// its tools) and a skill used only inside a subagent surfaces in the session.

// partitionClaudeAgents splits a session's rows into the main agent's rows and
// the per-subagent rows, keyed by agentId and kept in first-seen order so the
// nesting is deterministic.
func partitionClaudeAgents(rows []*ops.RawTrace) (main []*ops.RawTrace, order []string, sub map[string][]*ops.RawTrace) {
	sub = map[string][]*ops.RawTrace{}
	seen := map[string]bool{}
	for _, r := range rows {
		var ln struct {
			AgentID string `json:"agentId"`
		}
		_ = json.Unmarshal(r.Raw, &ln)
		if ln.AgentID == "" {
			main = append(main, r)
			continue
		}
		if !seen[ln.AgentID] {
			seen[ln.AgentID] = true
			order = append(order, ln.AgentID)
		}
		sub[ln.AgentID] = append(sub[ln.AgentID], r)
	}
	return main, order, sub
}

// agentLink is the resolved parent for one subagent: the span id of the Agent
// tool call that spawned it, and the subagent_type that call requested (for the
// subtree's display name).
type agentLink struct {
	toolSpanID   string
	subagentType string
}

// claudeSpawnTools are the tool names that spawn a subagent.
var claudeSpawnTools = map[string]bool{"Agent": true, "Task": true}

// claudeAgentLinks maps each subagent's agentId to the Agent tool span that
// spawned it. Claude records the agentId inside that call's tool_result, so the
// reliable link is agentId → the result's tool_use_id → the span with that call
// id (observed in real stores). When the result didn't echo the agentId, the
// spawn calls and subagents are zipped in order — both are first-seen ordered,
// so the Nth subagent pairs with the Nth unmatched spawn call. An unresolvable
// subagent gets an empty link and stays a top-level subtree (never dropped).
func claudeAgentLinks(spans []Span, mainRows []*ops.RawTrace, order []string) map[string]agentLink {
	callToSpan := map[string]string{}
	typeByCall := map[string]string{}
	var spawnCalls []string // call ids of spawn-tool spans, in span (time) order
	for i := range spans {
		sp := &spans[i]
		if name, _ := sp.Attributes["gen_ai.tool.name"].(string); !claudeSpawnTools[name] {
			continue
		}
		cid, _ := sp.Attributes["gen_ai.tool.call.id"].(string)
		if cid == "" {
			continue
		}
		callToSpan[cid] = sp.SpanID
		args, _ := sp.Attributes["gen_ai.tool.call.arguments"].(string)
		typeByCall[cid] = subagentTypeFromArgs(args)
		spawnCalls = append(spawnCalls, cid)
	}

	links := map[string]agentLink{}
	usedCall := map[string]bool{}
	// Pass 1: explicit agentId → spawning call via the call's tool_result.
	for _, agentID := range order {
		cid := claudeCallIDForAgent(mainRows, agentID)
		if cid != "" && callToSpan[cid] != "" {
			links[agentID] = agentLink{toolSpanID: callToSpan[cid], subagentType: typeByCall[cid]}
			usedCall[cid] = true
		}
	}
	// Pass 2: order-zip the remaining subagents to the remaining spawn calls.
	next := 0
	for _, agentID := range order {
		if _, ok := links[agentID]; ok {
			continue
		}
		for next < len(spawnCalls) && usedCall[spawnCalls[next]] {
			next++
		}
		if next < len(spawnCalls) {
			cid := spawnCalls[next]
			usedCall[cid] = true
			links[agentID] = agentLink{toolSpanID: callToSpan[cid], subagentType: typeByCall[cid]}
		} else {
			links[agentID] = agentLink{} // no spawn call found — stays a top-level subtree
		}
	}
	return links
}

// claudeCallIDForAgent finds the spawning call's tool_use_id by locating the
// tool_result line that carries this subagent's agentId. Gated on a byte
// pre-check so non-matching lines are not parsed.
func claudeCallIDForAgent(rows []*ops.RawTrace, agentID string) string {
	needle := []byte(agentID)
	for _, r := range rows {
		if !bytes.Contains(r.Raw, needle) {
			continue
		}
		var ln claudeLine
		if json.Unmarshal(r.Raw, &ln) != nil {
			continue
		}
		if _, _, results := parseUserContent(ln.Message); len(results) > 0 {
			for _, b := range results {
				if b.ToolUseID != "" {
					return b.ToolUseID
				}
			}
		}
	}
	return ""
}

// subagentTypeFromArgs reads the subagent_type from an Agent tool call's
// serialized arguments, or "" when absent.
func subagentTypeFromArgs(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var a struct {
		SubagentType string `json:"subagent_type"`
	}
	if json.Unmarshal([]byte(argsJSON), &a) != nil {
		return ""
	}
	return a.SubagentType
}

// subagentRootName names a subagent subtree's root span after the requested
// subagent_type, falling back to a generic label.
func subagentRootName(subagentType string) string {
	if subagentType != "" {
		return subagentType + " subagent"
	}
	return "Subagent"
}

// reparentRoots hangs a subagent walk's top-level turn roots under the spawning
// Agent tool span, so the subagent's whole tree nests beneath it. A "" parent
// (unresolvable spawn) leaves the roots at top level rather than dropping them.
func reparentRoots(spans []Span, parentSpanID string) {
	if parentSpanID == "" {
		return
	}
	for i := range spans {
		if spans[i].Kind == KindChain && spans[i].ParentSpanID == "" {
			spans[i].ParentSpanID = parentSpanID
		}
	}
}
