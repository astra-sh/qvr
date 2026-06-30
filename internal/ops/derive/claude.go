package derive

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("claude", claudeDeriver{}) }

// claudeDeriver reconstructs the Turn→Tool/Skill hierarchy from Claude Code
// transcript lines. Reconstruction (not a live hook stream) is what makes the
// projection regenerable: the same stored lines always rebuild the same spans.
//
// The semantic model is the standard agent-trace hierarchy: a user prompt opens
// a Turn (LLM span); assistant tool_use blocks become TOOL children;
// tool_result lines supply their output; a Skill tool-call is lifted into a
// dedicated SKILL span. It is derived from the full transcript, so it carries
// more than a hook-payload stream (e.g. reasoning, every assistant message).
//
// Skill-load evidence (verified against real ~/.claude/projects stores,
// 2026-06-11): a skill invocation is a tool_use named "Skill" with input
// {"skill":"<name>","args"?:"..."}; its tool_result is only the text
// "Launching skill: <name>" — no path. The load path arrives TWO LINES LATER
// as a type=user line flagged isMeta:true whose text block begins
//
//	Base directory for this skill: /abs/path/.claude/skills/<name>
//
// followed by the skill's SKILL.md body. That base-directory line is the
// artifact evidence: it is captured as skill.load_path on the pending SKILL
// span, which EnrichSkillIdentity resolves (symlink → worktree containment)
// to prove which locked artifact ran. isMeta lines are harness-injected
// content, NEVER user prompts — treating them as prompts both loses the load
// path and fabricates a turn whose "prompt" is the skill body. Transcripts
// also carry non-message line types (attachment, last-prompt, ai-title,
// mode); the type switch ignores them.
type claudeDeriver struct{}

// claudeLine is the subset of a Claude transcript JSONL line we read.
// gitBranch rides on every line (per Claude Code's transcript format) and
// feeds the unified session meta. isMeta marks harness-injected user lines
// (skill bodies, context attachments) as opposed to typed prompts.
type claudeLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	GitBranch string          `json:"gitBranch"`
	IsMeta    bool            `json:"isMeta"`
	AgentID   string          `json:"agentId"` // set on subagent (sidechain) lines; "" on the main agent's
	Message   json.RawMessage `json:"message"`
}

type claudeMessage struct {
	ID      string          `json:"id"` // API message id; repeats across per-block lines
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"` // string OR []block
	Usage   *claudeUsage    `json:"usage"`   // pointer: absence ≠ a zero-token record
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"` // extended-thinking block body (empty when encrypted)
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result content: string OR []block
	IsError   bool            `json:"is_error"`
}

func (claudeDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	sessionID := rows[0].SessionID.String()
	out := &Derivation{}
	// Claude Code writes one JSONL line per content block of an API message,
	// repeating the same message.id AND the same usage object on every line
	// (observed live, 2026-06-12: one message spanned 7 lines). Usage must
	// count once per message id or every token surface inflates ~2×.
	usageSeen := map[string]bool{}

	// Partition: the main agent's lines carry no agentId; each spawned subagent's
	// lines are tagged with its agentId. Claude Code writes a subagent to a
	// sibling subagents/agent-<id>.jsonl that ingests under the SAME session id
	// (its lines carry the parent's sessionId), so deriving everything in one
	// flat walk would splice a subagent's turns into the main timeline. Instead
	// each subagent becomes its own nested subtree under the Agent tool that
	// spawned it — which is also what finally surfaces a skill used only inside a
	// subagent (it now lands in the session's spans + meta).
	mainRows, subOrder, subRows := partitionClaudeAgents(rows)

	cp := profileFor("claude")
	w := &turnWalk{sessionID: sessionID, agentLabel: cp.model, rootName: cp.rootName, integration: cp.integration}
	deriveClaudeRows(w, mainRows, usageSeen, &out.Meta)
	out.Spans = w.spans

	links := claudeAgentLinks(out.Spans, mainRows, subOrder)
	for _, agentID := range subOrder {
		link := links[agentID]
		sw := &turnWalk{
			sessionID:   sessionID,
			idSalt:      agentID,
			agentLabel:  cp.model,
			rootName:    subagentRootName(link.subagentType),
			integration: cp.integration,
			runDepth:    1,
			agentType:   "subagent",
		}
		deriveClaudeRows(sw, subRows[agentID], usageSeen, nil)
		reparentRoots(sw.spans, link.toolSpanID)
		out.Spans = append(out.Spans, sw.spans...)
	}
	return out, nil
}

// deriveClaudeRows drives one walk (main or a subagent) over its rows, emitting
// its turn → tool/skill spans. meta is filled only for the main walk (GitBranch
// is a session property); a subagent walk passes nil.
func deriveClaudeRows(w *turnWalk, rows []*ops.RawTrace, usageSeen map[string]bool, meta *SessionMeta) {
	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln claudeLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue // a non-JSON / unexpected line is skipped, never fatal
		}
		if meta != nil && meta.GitBranch == "" && ln.GitBranch != "" {
			meta.GitBranch = ln.GitBranch
		}
		ts := parseISOMs(ln.Timestamp)

		switch ln.Type {
		case "user":
			claudeUserLine(w, &ln, ts)
		case "assistant":
			// ensure covers assistant output with no preceding prompt (e.g.
			// a session resumed mid-turn): a synthetic turn, nothing lost.
			w.ensure(ts)
			w.cur.absorbAssistant(ln.Message, ts, w.sessionID, usageSeen)
		}
	}
	w.flush()
}

// claudeUserLine folds one type=user line into the walk. A line bearing
// tool_result blocks is the OUTPUT of the current turn's pending tools, not a
// new prompt. Harness-injected content (isMeta) is never a prompt either —
// the one we mine is the skill-body injection: its leading "Base directory
// for this skill: <path>" line is the load-path evidence for the turn's
// pending SKILL span (see the type doc). Everything else is a real prompt and
// opens a new turn.
func claudeUserLine(w *turnWalk, ln *claudeLine, ts int64) {
	role, text, results := parseUserContent(ln.Message)
	if role == "" {
		return
	}
	if len(results) > 0 && w.cur != nil {
		for _, res := range results {
			w.cur.applyToolResult(res, ts)
		}
		return
	}
	if ln.IsMeta {
		if w.cur != nil {
			w.cur.applySkillBaseDir(text, w.sessionID)
		}
		return
	}
	w.open(ts)
	w.cur.prompt = text
}

// absorbAssistant folds one assistant line into the current turn: appends text,
// sums tokens, records the model, and turns each tool_use block into a TOOL
// (or SKILL) child span. usageSeen dedupes usage by message id — per-block
// lines repeat the same id with identical usage, so only the first counts
// (an id-less message can't be deduped and counts as its own).
func (t *turn) absorbAssistant(raw json.RawMessage, ts int64, sessionID string, usageSeen map[string]bool) {
	var msg claudeMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	if msg.Model != "" {
		t.model = msg.Model
	}
	if u := msg.Usage; u != nil && (msg.ID == "" || !usageSeen[msg.ID]) {
		if msg.ID != "" {
			usageSeen[msg.ID] = true
		}
		// input_tokens excludes cache reads/writes in the native format; the
		// turn total folds them back in so it means "context processed".
		t.addUsage(u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens, u.OutputTokens)
		t.addCacheRead(u.CacheReadInputTokens)
		t.addCacheCreation(u.CacheCreationInputTokens)
	}
	if ts > t.endMs {
		t.endMs = ts
	}

	for _, b := range decodeBlocks(msg.Content) {
		switch b.Type {
		case "text":
			if b.Text != "" {
				if t.output != "" {
					t.output += "\n"
				}
				t.output += b.Text
			}
		case "thinking":
			t.appendReasoning(b.Thinking)
		case "tool_use":
			t.addToolSpan(b, ts, sessionID)
		}
	}
}

// addToolSpan creates a child span for a tool_use block. A skill invocation
// (the "Skill" tool) is lifted into a dedicated SKILL span recording the loaded
// skill name and load time; everything else is a TOOL span.
func (t *turn) addToolSpan(b claudeBlock, ts int64, sessionID string) {
	inputJSON := ""
	if b.Input != nil {
		if data, err := json.Marshal(b.Input); err == nil {
			inputJSON = string(data)
		}
	}

	// Both skill loads and ordinary tool calls are OTel "execute_tool" spans;
	// a skill load additionally carries the Quiver skill.name extension tag.
	if skill := ops.SkillRefFromTool(b.Name, b.Input); skill != "" {
		attrs := map[string]any{
			"session.id":                 sessionID,
			"gen_ai.operation.name":      "execute_tool",
			"gen_ai.tool.name":           b.Name,
			"gen_ai.tool.call.id":        b.ID,
			"gen_ai.tool.call.arguments": inputJSON,
			"skill.name":                 skill, // Quiver extension
			// Claude only ever lifts its first-class Skill tool to a SKILL span
			// (it never path-signals authoring I/O), so the activation is always
			// a genuine tool call.
			SkillActivationKey: SkillActivationTool,
		}
		if t.model != "" {
			attrs["gen_ai.request.model"] = t.model // model cut for skill aggregations
		}
		sp := Span{
			Name:         spanDisplayName(KindSkill, b.Name, skill),
			Kind:         KindSkill,
			SpanID:       spanID(t.traceID, "skill", b.ID),
			TraceID:      t.traceID,
			ParentSpanID: t.llmSpanID,
			StartMs:      ts,
			EndMs:        ts,
			Attributes:   attrs,
		}
		t.tools = append(t.tools, sp)
		// Match a tool_result like any other call, so a Skill load that returns an
		// error/interrupt carries a real qvr.outcome. A clean load's success comes
		// from the base-directory injection instead (see attachSkillLoadPath) —
		// claude usually delivers the load that way, not as a tool_result.
		if b.ID != "" {
			t.pending[b.ID] = len(t.tools) - 1
		}
		return
	}

	attrs := map[string]any{
		"session.id":                 sessionID,
		"gen_ai.operation.name":      "execute_tool",
		"gen_ai.tool.name":           b.Name,
		"gen_ai.tool.call.id":        b.ID,
		"gen_ai.tool.call.arguments": inputJSON,
	}
	if t.model != "" {
		attrs["gen_ai.request.model"] = t.model
	}
	if d := toolDescription(b); d != "" {
		attrs["gen_ai.tool.description"] = d
	}
	// No path-signal skill attribution on claude. Unlike agents that have no
	// first-class skill mechanism (codex/cursor/…, where reading SKILL.md IS
	// the load), claude's authoritative load signals are the Skill tool
	// (handled above) and its base-directory injection (see applySkillBaseDir).
	// Scraping skills/<name> substrings out of ordinary Bash/Read/Edit/Write
	// text reclassifies authoring I/O — `cat`/`git add`/editing a skill's own
	// source files — as skill loads, inflating a skill's per-version activity
	// with tool calls that never used it (observed in real stores, 2026-06-24).
	sp := Span{
		Name:         displayToolName(b.Name),
		Kind:         KindTool,
		SpanID:       spanID(t.traceID, "tool", b.ID),
		TraceID:      t.traceID,
		ParentSpanID: t.llmSpanID,
		StartMs:      ts,
		EndMs:        ts,
		Attributes:   attrs,
	}
	t.tools = append(t.tools, sp)
	if b.ID != "" {
		t.pending[b.ID] = len(t.tools) - 1
	}
}

// claudeSkillBaseDirPrefix opens the harness-injected skill body (see the
// type doc): the remainder of its first line is the loaded skill's directory.
const claudeSkillBaseDirPrefix = "Base directory for this skill: "

// claudeSkillArgsTrailerRe matches the per-call arguments claude appends to a
// skill-body injection. The harness renders a Skill load as the verbatim
// SKILL.md body followed by a blank-line-separated "ARGUMENTS: <input>" trailer
// carrying the run's input (verified across real session stores, 2026-06:
// always "\n\n\nARGUMENTS: …", absent when the skill takes no args). That trailer
// is run-specific, so it must be stripped before the body feeds the content
// coordinate — otherwise two runs of the SAME skill version with different
// inputs digest to different hashes and the evolution-loop cohorts fragment to
// size one. The 3+ leading newlines (2+ blank lines) keep it from matching an
// ordinary single-blank-line markdown heading in the body itself.
var claudeSkillArgsTrailerRe = regexp.MustCompile(`(?s)\n{3,}ARGUMENTS:.*$`)

// applySkillBaseDir mines a harness-injected (isMeta) user text for the
// "Base directory for this skill: <path>" line and attaches the path as
// skill.load_path on the turn's most recent SKILL span that lacks one — the
// injection observed in real stores arrives immediately after the Skill
// tool_result, inside the same turn. The text AFTER that first line is the
// skill's verbatim SKILL.md body the harness loaded into context, so its digest
// is captured as the run-time content coordinate (see stampRunContentHash) —
// the bytes that actually ran, immune to a later switch/edit on disk.
func (t *turn) applySkillBaseDir(text, sessionID string) {
	if !strings.HasPrefix(text, claudeSkillBaseDirPrefix) {
		return
	}
	rest := text[len(claudeSkillBaseDirPrefix):]
	path, body, _ := strings.Cut(rest, "\n")
	path = strings.TrimSpace(path)
	// Drop the run-specific "ARGUMENTS: …" trailer so only the skill body — the
	// version-identifying bytes — enters the content coordinate.
	body = claudeSkillArgsTrailerRe.ReplaceAllString(body, "")
	// The base-dir path encodes the skill name (…/skills/<name>); pass it so
	// two Skill calls in one assistant message (parallel tool use) each get
	// their OWN injection — a nameless reverse search would swap them. A path
	// with no skills/<name> segment yields "" and matches any pending span.
	name, _, _ := pathSkillRef(path, nil)
	if t.attachSkillLoadPath(name, path) {
		t.attachSkillBody(name, body)
		return
	}
	// No pending SKILL span — the harness injected this skill directly, with no
	// preceding Skill tool call. Create the span from the injection so the load
	// is attributed rather than lost (178 such loads across the real corpus).
	t.addInjectedSkill(name, path, body, sessionID)
}

// addInjectedSkill creates a SKILL span for a harness-injected skill load that
// had no preceding Skill tool call (skill.activation = injection). It mirrors
// the tool-load span shape so downstream attribution treats it identically, and
// stamps the run-time content coordinate from the injected body.
func (t *turn) addInjectedSkill(name, path, body, sessionID string) {
	if name == "" {
		return // a base-dir line with no resolvable skills/<name> is not attributable
	}
	attrs := map[string]any{
		"session.id":            sessionID,
		"gen_ai.operation.name": "execute_tool",
		"gen_ai.tool.name":      "Skill",
		"skill.name":            name,
		SkillActivationKey:      SkillActivationInjection,
		"skill.load_path":       path,
		// A harness injection is positive evidence the skill loaded.
		OutcomeKey: OutcomeSuccess,
	}
	if t.model != "" {
		attrs["gen_ai.request.model"] = t.model
	}
	sp := Span{
		Name:         spanDisplayName(KindSkill, "Skill", name),
		Kind:         KindSkill,
		SpanID:       spanID(t.traceID, "skill", "inject#"+strconv.Itoa(len(t.tools))),
		TraceID:      t.traceID,
		ParentSpanID: t.llmSpanID,
		StartMs:      t.endMs,
		EndMs:        t.endMs,
		Attributes:   attrs,
	}
	t.tools = append(t.tools, sp)
	stampRunContentHash(&sp, body)
	t.tools[len(t.tools)-1] = sp
}

// applyToolResult attaches a tool_result to the tool span awaiting it.
func (t *turn) applyToolResult(b claudeBlock, ts int64) {
	idx, ok := t.pending[b.ToolUseID]
	if !ok {
		return
	}
	sp := &t.tools[idx]
	result := decodeToolResultText(b.Content)
	sp.Attributes["gen_ai.tool.call.result"] = result
	if b.IsError {
		sp.Attributes["error.type"] = "tool_failure"
	}
	sp.Attributes[OutcomeKey] = classifyOutcome(result, b.IsError)
	if ts > sp.StartMs {
		sp.EndMs = ts
	}
	delete(t.pending, b.ToolUseID)
}

// providerName maps a model id to its OTel gen_ai.provider.name, or "" when
// unknown.
func providerName(model string) string {
	switch {
	case strings.HasPrefix(model, "claude"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"):
		return "openai"
	case strings.HasPrefix(model, "gemini"):
		return "gcp.gemini"
	default:
		return ""
	}
}

// --- content decoding helpers ---

// parseUserContent classifies a user message. Returns (role, promptText,
// toolResults). promptText is set when the content is a plain prompt; results
// is set when the content carries tool_result blocks.
func parseUserContent(raw json.RawMessage) (role, text string, results []claudeBlock) {
	var msg claudeMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", "", nil
	}
	role = msg.Role
	if role == "" {
		role = "user"
	}
	// content as a plain string → a prompt.
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return role, s, nil
	}
	// content as an array → text blocks (prompt) and/or tool_result blocks.
	for _, b := range decodeBlocks(msg.Content) {
		switch b.Type {
		case "tool_result":
			results = append(results, b)
		case "text":
			if b.Text != "" {
				if text != "" {
					text += "\n"
				}
				text += b.Text
			}
		}
	}
	return role, text, results
}

func decodeBlocks(raw json.RawMessage) []claudeBlock {
	var blocks []claudeBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// decodeToolResultText renders a tool_result's content (string or block array)
// to text.
func decodeToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	out := ""
	for _, b := range decodeBlocks(raw) {
		if b.Type == "text" && b.Text != "" {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// toolDescription gives a short human label per tool, mirroring the reference's
// per-tool description logic.
func toolDescription(b claudeBlock) string {
	get := func(k string) string {
		if v, ok := b.Input[k].(string); ok {
			return v
		}
		return ""
	}
	switch b.Name {
	case "Bash":
		return clip(get("command"), 200)
	case "Read", "Write", "Edit", "Glob":
		if p := get("file_path"); p != "" {
			return clip(p, 200)
		}
		return clip(get("pattern"), 200)
	case "Grep":
		return clip("grep: "+get("pattern"), 200)
	case "WebFetch":
		return clip(get("url"), 200)
	case "WebSearch":
		return clip(get("query"), 200)
	default:
		return ""
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
