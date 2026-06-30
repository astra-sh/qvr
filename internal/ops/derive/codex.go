package derive

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("codex", codexDeriver{}) }

// codexDeriver reconstructs the Turn→Tool/Skill hierarchy from a Codex CLI
// rollout transcript. Like the claude deriver it is PURE — the same stored
// lines always rebuild the same spans — so the projection is regenerable.
//
// Codex's rollout JSONL is a flat stream of records, each a top-level
// {"timestamp","type","payload"} envelope. Four envelope types matter:
//
//	session_meta  — once per session (model provider, cwd, cli version)
//	turn_context  — per turn: carries the model used for the turn
//	event_msg     — harness events; payload.type is the subtype:
//	                  task_started   (turn opens)
//	                  user_message   (the real prompt — clean, no injected context)
//	                  token_count    (per-request usage; info.last_token_usage)
//	                  agent_message  (final assistant text)
//	                  task_complete  (turn closes)
//	response_item — model/tool I/O; payload.type is the subtype:
//	                  message            (role user/developer = injected context;
//	                                      role assistant = output text)
//	                  function_call      (a tool call: name, arguments, call_id)
//	                  function_call_output (its result, keyed by call_id)
//
// The prompt is taken from the event_msg "user_message" event rather than the
// response_item user messages, because the latter also carry injected context
// (AGENTS.md, environment_context, the developer instructions block) that is
// not what the user typed.
//
// Skill attribution uses Codex's OWN native mechanism, not anything Quiver-
// specific: Codex injects a <skills_instructions> block (a developer message)
// listing every available skill with its name + SKILL.md path, and tells the
// model to "open its SKILL.md" to use it. So a skill use surfaces as the model
// reading a file under a `skills/<name>/` (or `rules/<name>/`) directory — e.g.
// `sed -n 1,40p .codex/skills/code-review/SKILL.md`. We parse the injected list
// for the authoritative set of skill names, then attribute any tool command
// that touches one of those skill paths. This does not depend on `qvr` being on
// the PATH or on a `qvr read` call — it is exactly the signal Codex itself
// defines, so it keeps working for skills installed by any tool.
type codexDeriver struct{}

// codexLine is the rollout envelope. Payload is left raw and decoded per type.
type codexLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// codexPayload is the union of the payload fields we read across envelope types.
// Only the fields relevant to the current envelope are populated; the rest stay
// zero.
type codexPayload struct {
	Type string `json:"type"` // event_msg / response_item subtype

	// turn_context (and, when present, session_meta)
	Model         string `json:"model"`
	ModelProvider string `json:"model_provider"`

	// session_meta: repo context (feeds the unified session meta)
	Git *codexGitInfo `json:"git"`

	// response_item: message
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // []{type,text}

	// response_item: function_call
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string of the call args
	Input     string `json:"input"`     // custom_tool_call body (e.g. apply_patch patch text)
	CallID    string `json:"call_id"`

	// response_item: function_call_output
	Output json.RawMessage `json:"output"` // string (usually) or object

	// event_msg: user_message / agent_message
	Message string `json:"message"`

	// event_msg: task_complete
	LastAgentMessage string `json:"last_agent_message"`

	// event_msg: token_count
	Info *codexTokenInfo `json:"info"`

	// response_item: reasoning — a model thinking step. summary carries the
	// human-readable text when present; encrypted_content (which we never decode)
	// carries the opaque full reasoning, so a summary-less item contributes no text.
	Summary []codexReasoningPart `json:"summary"`
}

// codexReasoningPart is one block of a reasoning item's summary.
type codexReasoningPart struct {
	Type string `json:"type"` // "summary_text"
	Text string `json:"text"`
}

type codexTokenInfo struct {
	Last codexUsage `json:"last_token_usage"`
}

// codexGitInfo is the session_meta payload's git block.
type codexGitInfo struct {
	Branch string `json:"branch"`
}

type codexUsage struct {
	InputTokens       int `json:"input_tokens"` // total; includes cached_input_tokens
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

// codexBlock is one content block of a message (input_text / output_text).
type codexBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (codexDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	st := &codexState{
		turnWalk: turnWalk{
			sessionID:   rows[0].SessionID.String(),
			agentLabel:  profileFor("codex").model,
			rootName:    profileFor("codex").rootName,
			integration: profileFor("codex").integration,
		},
		// valid is the authoritative set of skill names Codex injected via
		// <skills_instructions> for this session. Empty until that block is seen;
		// while empty, skill-path detection falls back to accepting any
		// well-formed skills/<name> segment (a still-native signal).
		valid: map[string]bool{},
		// catalogPath maps each injected skill name to the authoritative file
		// path Codex declared for it in <skills_instructions> — the durable,
		// sha-bearing load coordinate (see codexSkillEntryRe).
		catalogPath: map[string]string{},
	}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln codexLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue
		}
		ts := parseISOMs(ln.Timestamp)
		var p codexPayload
		if len(ln.Payload) > 0 {
			_ = json.Unmarshal(ln.Payload, &p)
		}

		switch ln.Type {
		case "session_meta", "turn_context":
			st.setModel(p.Model)
			if out.Meta.GitBranch == "" && p.Git != nil && p.Git.Branch != "" {
				out.Meta.GitBranch = p.Git.Branch
			}
		case "event_msg":
			st.handleEventMsg(p, ts)
		case "response_item":
			st.handleResponseItem(p, ts)
		}
	}
	st.flush()
	st.applyCatalogPaths()
	st.applyNarratedSkills()
	out.Spans = st.spans
	return out, nil
}

// codexSkillIntent{Before,After}Re match the agent narrating a skill use in
// either word order: the name before "skill" ("using the `code-review` skill",
// "slugify-title skill") or after it ("Used skill: `slugify-title`", "skill
// slugify-title"). Both captured names are gated against the injected catalog
// (valid) in applyNarratedSkills, so a passing phrase like "the right skill" or
// "skill: foo" can't attribute — only a real, available skill name does. This
// is the weakest signal and only ADDS a load the path signal missed; the
// catalog gate is what keeps the broad match from over-attributing.
var (
	codexSkillIntentBeforeRe = regexp.MustCompile(`(?i)[\x60'"]?([a-z][a-z0-9-]{1,63})[\x60'"]?\s+skill\b`)
	codexSkillIntentAfterRe  = regexp.MustCompile(`(?i)\bskills?[:\s]+[\x60'"]?([a-z][a-z0-9-]{1,63})[\x60'"]?`)
)

// narratedSkillNames extracts every skill name the agent named in a message,
// in either word order. Gating against the catalog happens later.
func narratedSkillNames(msg string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{codexSkillIntentBeforeRe, codexSkillIntentAfterRe} {
		for _, m := range re.FindAllStringSubmatch(msg, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// applyNarratedSkills adds an implicit SKILL span for any catalog skill the
// agent narrated using but that left no tool/path evidence (the advisory case:
// the model followed an injected skill without re-reading its SKILL.md). Gated
// hard: the name must be in the injected catalog AND not already attributed by a
// stronger signal, so this never double-counts or invents a skill.
func (st *codexState) applyNarratedSkills() {
	if len(st.narrated) == 0 {
		return
	}
	attributed := map[string]bool{}
	for i := range st.spans {
		if st.spans[i].Kind != KindSkill {
			continue
		}
		if n, _ := st.spans[i].Attributes["skill.name"].(string); n != "" {
			attributed[n] = true
		}
	}
	added := map[string]bool{}
	for _, nz := range st.narrated {
		if !st.valid[nz.name] || attributed[nz.name] || added[nz.name] {
			continue
		}
		parent := st.llmSpanForTime(nz.ts)
		if parent == nil {
			continue // no model span to hang it under (shouldn't happen post-flush)
		}
		added[nz.name] = true
		attrs := map[string]any{
			"session.id":            st.sessionID,
			"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name":      "Skill",
			"skill.name":            nz.name,
			SkillActivationKey:      SkillActivationImplicit,
			OutcomeKey:              OutcomeSuccess,
		}
		if cat := st.catalogPath[nz.name]; cat != "" {
			attrs["skill.load_path"] = cat
		}
		st.spans = append(st.spans, Span{
			Name:         spanDisplayName(KindSkill, "Skill", nz.name),
			Kind:         KindSkill,
			SpanID:       spanID(parent.TraceID, "skill", "implicit#"+nz.name),
			TraceID:      parent.TraceID,
			ParentSpanID: parent.SpanID,
			StartMs:      nz.ts,
			EndMs:        nz.ts,
			Attributes:   attrs,
		})
	}
}

// llmSpanForTime returns the model span whose window contains ts (the turn the
// narration belongs to), falling back to the last model span.
func (st *codexState) llmSpanForTime(ts int64) *Span {
	var last *Span
	for i := range st.spans {
		if st.spans[i].Kind != KindLLM {
			continue
		}
		last = &st.spans[i]
		if ts >= st.spans[i].StartMs && ts <= st.spans[i].EndMs {
			return last
		}
	}
	return last
}

// applyCatalogPaths upgrades each SKILL span's load_path to the authoritative
// store-worktree path Codex declared in <skills_instructions>, when the span's
// own scraped path is not itself a store-worktree path. Codex sessions routinely
// record a weaker shadow path (a user "read .codex/skills/<name>/SKILL.md", a
// symlink copy) at call time while the catalog carries the sha-keyed worktree
// location; preferring the catalog path pins the version in the captured bytes
// so the run stays attributable after the skill is uninstalled. Strictly gated:
// the path comes only from a parsed catalog entry (never arbitrary transcript
// text), and an existing store-path load_path (call-time proof) is left intact.
func (st *codexState) applyCatalogPaths() {
	if len(st.catalogPath) == 0 {
		return
	}
	for i := range st.spans {
		sp := &st.spans[i]
		if sp.Kind != KindSkill {
			continue
		}
		name, _ := sp.Attributes["skill.name"].(string)
		cat := st.catalogPath[name]
		if cat == "" {
			continue
		}
		if _, _, sha := storeWorktreeIdentity(cat); sha == "" {
			continue // catalog path is not a sha-keyed worktree — nothing durable to add
		}
		cur, _ := sp.Attributes["skill.load_path"].(string)
		if _, _, curSha := storeWorktreeIdentity(cur); curSha != "" {
			continue // already pinned by a store-worktree path; keep call-time evidence
		}
		sp.Attributes["skill.load_path"] = cat
	}
}

// codexNarration is one (timestamp, skill name) the agent narrated using, held
// until the catalog is fully learned and resolved in applyNarratedSkills.
type codexNarration struct {
	ts   int64
	name string
}

// codexState is the shared turn walk plus the learned skill-name set.
type codexState struct {
	turnWalk
	valid       map[string]bool   // skill names injected via <skills_instructions>
	catalogPath map[string]string // skill name → authoritative declared file path
	narrated    []codexNarration  // agent-narrated skill uses, resolved at end
}

// handleEventMsg processes an event_msg envelope: turn open/close, the clean
// prompt, accumulated usage, and the final assistant text.
func (st *codexState) handleEventMsg(p codexPayload, ts int64) {
	switch p.Type {
	case "task_started":
		st.open(ts)
	case "user_message":
		st.ensure(ts)
		if st.cur.prompt == "" {
			st.cur.prompt = p.Message
		}
	case "agent_message":
		st.ensure(ts)
		if p.Message != "" {
			// The final assistant text is also persisted as a response_item
			// `message` (role assistant), which handleResponseItem folds into
			// the turn output. That response_item is the source of truth, so
			// here we only mine narrated skill uses — appending p.Message too
			// would double the output (the agent_message event mirrors it).
			for _, name := range narratedSkillNames(p.Message) {
				st.narrated = append(st.narrated, codexNarration{ts: ts, name: name})
			}
		}
		st.cur.bump(ts)
	case "token_count":
		if st.cur != nil && p.Info != nil {
			st.cur.addUsage(p.Info.Last.InputTokens, p.Info.Last.OutputTokens)
			// Codex reports cache reads only (no creation counter).
			st.cur.addCacheRead(p.Info.Last.CachedInputTokens)
		}
	case "task_complete":
		st.ensure(ts)
		if st.cur.output == "" && p.LastAgentMessage != "" {
			st.cur.appendOutput(p.LastAgentMessage)
		}
		st.cur.bump(ts)
		st.flush()
	}
}

// handleResponseItem processes a response_item envelope: assistant output text,
// the injected <skills_instructions> registry, tool calls, and their results.
func (st *codexState) handleResponseItem(p codexPayload, ts int64) {
	switch p.Type {
	case "message":
		blocks := decodeCodexBlocks(p.Content)
		// Any message (usually the developer one) may carry the
		// <skills_instructions> registry; learn the skill names + paths from it.
		st.learnSkills(blocks)
		// Only assistant output is the turn's text. User/developer
		// messages here are injected context, not the prompt.
		if p.Role == "assistant" {
			st.ensure(ts)
			for _, b := range blocks {
				if b.Text != "" {
					st.cur.appendOutput(b.Text)
				}
			}
			st.cur.bump(ts)
		}
	case "reasoning":
		st.ensure(ts)
		for _, s := range p.Summary {
			st.cur.appendReasoning(s.Text)
		}
		st.cur.bump(ts)
	case "function_call":
		st.ensure(ts)
		st.cur.addCodexTool(p, ts, st.sessionID, st.valid)
		st.cur.bump(ts)
	case "function_call_output":
		if st.cur != nil {
			st.cur.applyResult(p.CallID, decodeCodexOutput(p.Output), ts, codexOutputIsError(p.Output))
		}
	case "custom_tool_call":
		// Codex's freeform tools (e.g. apply_patch) arrive as custom_tool_call
		// with the call body in `input`, not `arguments`. Without this case the
		// file-writing call is silently dropped from the trace.
		st.ensure(ts)
		st.cur.addCodexCustomTool(p, ts, st.sessionID, st.valid)
		st.cur.bump(ts)
	case "custom_tool_call_output":
		if st.cur != nil {
			st.cur.applyResult(p.CallID, decodeCodexOutput(p.Output), ts, codexOutputIsError(p.Output))
		}
	}
}

// learnSkills records each skill's name and authoritative file path from any
// <skills_instructions> block among the message blocks. The path (a sha-keyed
// worktree location for a qvr-managed skill) is the durable load coordinate
// applyCatalogPaths later stamps onto the skill's spans.
func (st *codexState) learnSkills(blocks []codexBlock) {
	for _, b := range blocks {
		if !strings.Contains(b.Text, skillsInstructionsTag) {
			continue
		}
		for name, path := range parseCodexSkills(b.Text) {
			st.valid[name] = true
			if path != "" {
				if _, seen := st.catalogPath[name]; !seen {
					st.catalogPath[name] = path
				}
			}
		}
	}
}

// addCodexTool turns a function_call into a child span. Skill usage is detected
// the way Codex itself defines it (see the type doc): a tool command that reads
// a file under a `skills/<name>/` (or `rules/<name>/`) path is attributed to
// that skill via the skill.name extension; a literal "Skill" tool-call wins.
// valid is the authoritative skill-name set from <skills_instructions>.
func (t *turn) addCodexTool(p codexPayload, ts int64, sessionID string, valid map[string]bool) {
	args := map[string]any{}
	if p.Arguments != "" {
		_ = json.Unmarshal([]byte(p.Arguments), &args)
	}
	cmd := commandFromArgs(args)
	ref := resolveSkillRef(p.Name, args, cmd, "", valid)
	t.addToolInvocation(p.Name, p.CallID, p.Arguments, cmd, ref, ts, sessionID)
}

// addCodexCustomTool turns a custom_tool_call (e.g. apply_patch) into a child
// span. Its body rides in `input` (raw patch text), not a JSON `arguments`
// string, so it is passed straight through; skill attribution still works if
// the call touches a path under a skills/<name>/ directory.
func (t *turn) addCodexCustomTool(p codexPayload, ts int64, sessionID string, valid map[string]bool) {
	ref := resolveSkillRef(p.Name, nil, "", p.Input, valid)
	t.addToolInvocation(p.Name, p.CallID, p.Input, "", ref, ts, sessionID)
}

const skillsInstructionsTag = "<skills_instructions>"

// codexSkillEntryRe matches one entry of the injected skill list, e.g.
//
//   - code-review: Review pending changes ... (file: /path/.../skills/code-review/SKILL.md)
//
// capturing the skill name AND the authoritative file path Codex declares for
// it. That path is the install location — for a qvr-managed skill a sha-keyed
// worktree path (…/worktrees/<reg>/<skill>/<sha7>/…) — so it pins the version in
// the captured transcript bytes and stays recoverable even after the skill is
// uninstalled and its directory is gone. The path is the durable load coordinate
// the scraped command path (a shadow copy like .codex/skills/<name>/SKILL.md)
// usually lacks. Capture is anchored to the catalog line's "(file:" marker, so a
// message that merely prints a worktree path elsewhere is never matched.
var codexSkillEntryRe = regexp.MustCompile(`(?m)^\s*-\s+([a-z0-9][a-z0-9-]{0,63}):.*\(file:\s*([^)]+?)\s*\)`)

// parseCodexSkills extracts each skill's name and its declared file path from a
// <skills_instructions> block. The path is "" only when the entry omits it.
func parseCodexSkills(text string) map[string]string {
	out := map[string]string{}
	for _, m := range codexSkillEntryRe.FindAllStringSubmatch(text, -1) {
		out[m[1]] = strings.TrimPrefix(strings.TrimSpace(m[2]), "file://")
	}
	return out
}

func decodeCodexBlocks(raw json.RawMessage) []codexBlock {
	var blocks []codexBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// decodeCodexOutput renders a function_call_output payload (a string, or a
// {output:...} / {content:...} object) to text.
func decodeCodexOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Output  string `json:"output"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Output != "" {
			return obj.Output
		}
		if obj.Content != "" {
			return obj.Content
		}
	}
	return string(raw)
}

// codexOutputIsError reports whether a function_call_output carries a failure
// signal. Codex does not set a separate error flag on the result envelope the
// way Claude's tool_result does — it folds the outcome into the output payload:
// an exec result is an object {output, metadata:{exit_code}} (commonly
// double-encoded as a JSON string), and some tools attach an explicit success
// bool. Without reading it, every codex tool/skill result derived to success and
// a codex session's observed outcome could never be failure/blocked — wrong for
// the one non-Claude agent with native skill detection.
//
// Defensive by construction: a plain-string output (no structured envelope) and
// any shape lacking both signals report no error, so the common case is
// unchanged — only an explicit success:false or a non-zero exit_code flips it.
func codexOutputIsError(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	// The envelope may be a JSON object directly, or a JSON STRING containing
	// that object (codex double-encodes exec results); unwrap one string layer.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var obj struct {
		Success  *bool `json:"success"`
		Metadata *struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return false // plain text output: no structured failure signal
	}
	switch {
	case obj.Success != nil:
		return !*obj.Success
	case obj.Metadata != nil && obj.Metadata.ExitCode != nil:
		return *obj.Metadata.ExitCode != 0
	default:
		return false
	}
}
