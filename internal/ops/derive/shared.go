package derive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
)

// Shared format helpers for the per-agent derivers. Every deriver walks a
// different native record shape, but they all need the same primitives:
// flexible timestamp parsing, the path-based skill signal, and the
// turn → tool/skill span plumbing. Keeping these here is what keeps each
// deriver a thin format adapter.

// parseISOMs parses an ISO-8601 timestamp to epoch ms, or 0.
func parseISOMs(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// flexTimeMs normalizes the timestamp encodings agent transcripts use — an
// ISO-8601 string, or a numeric epoch in seconds, milliseconds, or
// microseconds — to epoch ms.
func flexTimeMs(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseISOMs(s)
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
		return 0
	}
	switch {
	case n > 1e15: // microseconds
		return int64(n / 1e3)
	case n > 1e12: // milliseconds
		return int64(n)
	default: // seconds
		return int64(n * 1e3)
	}
}

// skillDirPathRe matches a skill-directory reference inside any text (a shell
// command, serialized tool arguments, a file path), capturing the whole path
// token (group 1) and the skill name (group 2). The `skills/<name>/` or
// `rules/<name>/` segment is shared by every install form — the absolute
// ~/.quiver worktree path and the relative agent-dir symlink both contain it —
// so the captured token is the real path the tool referenced, which
// EnrichSkillIdentity resolves to verify the loaded artifact.
//
// The token classes exclude JSON syntax (quotes, braces, commas, backslashes)
// in addition to whitespace: paths are routinely matched inside compact
// serialized tool arguments ({"file_path":"/x/skills/y/SKILL.md"}), where a
// \S* token would swallow the surrounding JSON and yield an unresolvable
// "path" (observed in real claude stores, 2026-06-11).
var skillDirPathRe = regexp.MustCompile(`([^\s"'\\{}\[\],]*(?:skills|rules)/([a-z0-9][a-z0-9-]{0,63})(?:/[^\s"'\\{}\[\],]*)?)`)

// A direct reference into qvr's immutable store — .quiver/worktrees/<registry>/
// <skill>/<sha7>/... — is parsed by the shared worktreePathRe grammar (see
// worktree_path.go). Agents resolve the agent-dir symlink before reading
// (observed in real codex rollouts, 2026-06-11: `sed -n 1,220p
// /Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/SKILL.md`), and a LOCAL
// install's subtree has no skills/<name>/ segment for skillDirPathRe to find,
// so the store layout itself must be a recognized signal — it is also the
// strongest one, pinning registry+skill+sha directly.

// pathSkillRef reports the skill a tool invocation touches, whether the access
// is the skill's SKILL.md (its "load"), and the path token actually
// referenced. When valid is non-empty only those names match (an agent that
// announces its skill set, like codex); when empty any well-formed
// skills/<name> segment is accepted — still a native, qvr-independent signal.
func pathSkillRef(text string, valid map[string]bool) (name string, isLoad bool, loadPath string) {
	if text == "" {
		return "", false, ""
	}
	// The store-layout signal first: a resolved worktree path is unambiguous
	// (registry/skill/sha in the path) and is NOT gated on the valid set —
	// a path into qvr's own store identifies the skill regardless of what the
	// agent announced.
	for _, wm := range parseWorktreePaths(text) {
		if !isResolvedPath(wm.token) {
			continue
		}
		return wm.skill, strings.HasSuffix(wm.token, "/SKILL.md"), wm.token
	}
	for _, m := range skillDirPathRe.FindAllStringSubmatch(text, -1) {
		path, n := m[1], m[2]
		if len(valid) > 0 && !valid[n] {
			continue
		}
		if !isResolvedPath(path) {
			continue
		}
		return n, strings.Contains(text, n+"/SKILL.md"), path
	}
	return "", false, ""
}

// isResolvedPath reports whether a captured token is a concrete filesystem
// path rather than an unexpanded shell fragment. Tokens scraped from command
// text routinely carry shell variables or globs — e.g. "$REG/skills/clean-skill"
// out of `mkdir -p $REG/skills/clean-skill` — that never name an installed
// skill; treating them as load evidence invents skills that were never
// materialized (observed in real claude stores, 2026-06-24). A real load path
// (the agent-dir symlink or the ~/.quiver worktree) contains none of these.
func isResolvedPath(tok string) bool {
	return tok != "" && !strings.ContainsAny(tok, "$*?`")
}

// skillRef is one resolved skill attribution for a tool invocation.
type skillRef struct {
	name     string
	isLoad   bool   // the invocation opened the skill's SKILL.md (its "load")
	loadPath string // the path token actually referenced, for verification
	// provenance is how the skill was identified — SkillActivationTool (a
	// first-class skill tool call) or SkillActivationPath (a scraped file
	// path). Empty when name is empty. Stamped onto a SKILL span as
	// skill.activation so genuine activations are distinguishable from
	// file-touches.
	provenance string
}

// resolveSkillRef attributes one tool invocation to a skill: a literal "Skill"
// tool-call wins (the agent's own first-class skill mechanism); otherwise the
// path signal over the invocation's command text, then its serialized
// arguments. valid optionally restricts the accepted skill-name set. When the
// caller has only serialized arguments (OpenAI-style function calls carry a
// JSON string — hermes, codex), they are parsed here so name-keyed skill
// tools like hermes's skill_view still resolve.
func resolveSkillRef(toolName string, args map[string]any, cmdText, argsJSON string, valid map[string]bool) skillRef {
	if args == nil && argsJSON != "" {
		args = map[string]any{}
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}
	if name := ops.SkillRefFromTool(toolName, args); name != "" {
		// A skill tool invoked WITH a file argument reads a supporting file
		// (hermes's skill_view(name, file_path)) — attribute it to the skill
		// without counting a load, mirroring how path-signal file reads stay
		// TOOL spans.
		return skillRef{name: name, isLoad: !skillToolReadsFile(args), provenance: SkillActivationTool}
	}
	name, isLoad, loadPath := pathSkillRef(cmdText, valid)
	if name == "" {
		name, isLoad, loadPath = pathSkillRef(argsJSON, valid)
	}
	prov := ""
	if name != "" {
		prov = SkillActivationPath
	}
	return skillRef{name: name, isLoad: isLoad, loadPath: loadPath, provenance: prov}
}

// skillToolReadsFile reports whether a skill tool-call's arguments target a
// supporting file rather than the skill itself.
func skillToolReadsFile(args map[string]any) bool {
	for _, k := range []string{"file_path", "file"} {
		if v, ok := args[k].(string); ok && v != "" {
			return true
		}
	}
	return false
}

// addToolInvocation turns one tool invocation into a child span. A skill load
// (ref.isLoad) lifts to a SKILL span; touching other files under a skill dir
// stays a TOOL span that still carries skill.name, so the action is attributed
// without inventing a load.
func (t *turn) addToolInvocation(toolName, callID, argsJSON, cmdText string, ref skillRef, ts int64, sessionID string) {
	attrs := map[string]any{
		"session.id":                 sessionID,
		"gen_ai.operation.name":      "execute_tool",
		"gen_ai.tool.name":           toolName,
		"gen_ai.tool.call.id":        callID,
		"gen_ai.tool.call.arguments": argsJSON,
	}
	// The turn's model rides on its tool/skill children so skill aggregations
	// can cut by model ("skill A on opus vs skill B on fable") without a
	// parent-span join.
	if t.model != "" {
		attrs["gen_ai.request.model"] = t.model
	}
	if cmdText != "" {
		attrs["gen_ai.tool.description"] = clip(cmdText, 200)
	}
	kind := KindTool
	idKind := "tool"
	if ref.name != "" {
		attrs["skill.name"] = ref.name // Quiver extension
		// The actual file path the invocation referenced; EnrichSkillIdentity
		// uses it to attribute the artifact that loaded rather than
		// name-matching the lock (#149).
		if ref.loadPath != "" {
			attrs["skill.load_path"] = ref.loadPath
		}
		if ref.isLoad {
			kind = KindSkill
			idKind = "skill"
			if ref.provenance != "" {
				attrs[SkillActivationKey] = ref.provenance
			}
		}
	}

	// Fall back to a per-turn unique suffix when the format omits a call id:
	// an empty suffix would make every id-less tool span in the turn collide.
	callKey := callID
	if callKey == "" {
		callKey = "tool#" + strconv.Itoa(len(t.tools))
	}
	sp := Span{
		Name:         spanDisplayName(kind, toolName, ref.name),
		Kind:         kind,
		SpanID:       spanID(t.traceID, idKind, callKey),
		TraceID:      t.traceID,
		ParentSpanID: t.llmSpanID,
		StartMs:      ts,
		EndMs:        ts,
		Attributes:   attrs,
	}
	t.tools = append(t.tools, sp)
	if callID != "" {
		t.pending[callID] = len(t.tools) - 1
	}
}

// spanDisplayName is the clean tree title for a tool/skill child: the skill name
// for a SKILL span, otherwise the short tool name (displayToolName). The exact
// tool name stays on gen_ai.tool.name for filtering, so shortening the title
// loses nothing queryable.
func spanDisplayName(kind, toolName, skillName string) string {
	if kind == KindSkill && skillName != "" {
		return skillName
	}
	return displayToolName(toolName)
}

// displayToolName shortens a raw tool name for a span title. MCP tools arrive as
// mcp__<server>__<tool> (sometimes mcp__plugin_<plugin>_<server>__<tool>); show
// the final <tool> segment — the action — dropping the transport/server prefix.
// Every other tool name is already short (Bash, Read, …) and passes through.
func displayToolName(name string) string {
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		parts := strings.Split(rest, "__")
		last := parts[len(parts)-1]
		if last != "" {
			return last
		}
	}
	return name
}

// addCommandTool is the common-case wrapper: resolve the skill attribution
// from the invocation itself, then emit the span.
func (t *turn) addCommandTool(toolName, callID, argsJSON, cmdText string, ts int64, sessionID string, valid map[string]bool) {
	ref := resolveSkillRef(toolName, nil, cmdText, argsJSON, valid)
	t.addToolInvocation(toolName, callID, argsJSON, cmdText, ref, ts, sessionID)
}

// applyResult attaches a tool invocation's output to the span awaiting it.
func (t *turn) applyResult(callID, result string, ts int64, isError bool) {
	idx, ok := t.pending[callID]
	if !ok {
		return
	}
	applyResultTo(&t.tools[idx], result, ts, isError)
	delete(t.pending, callID)
}

// applyResultTo stamps a result onto one span. A SKILL span that lacked path
// evidence at call time gets a second chance here: name-only skill tools
// (hermes's skill_view, opencode's skill, …) inline the loaded artifact's
// real directory in their RESULT, so the result text is mined for it.
func applyResultTo(sp *Span, result string, ts int64, isError bool) {
	sp.Attributes["gen_ai.tool.call.result"] = result
	if isError {
		sp.Attributes["error.type"] = "tool_failure"
	}
	sp.Attributes[OutcomeKey] = classifyOutcome(result, isError)
	if ts > sp.StartMs {
		sp.EndMs = ts
	}
	mineSkillLoadPath(sp, result)
	stampPathLoadContentHash(sp, result, isError)
}

// stampPathLoadContentHash records the evolution loop's content coordinate for
// a by-path skill load (codex/cursor/… reading a skill's SKILL.md): for that
// activation the tool RESULT is the verbatim file body the agent read, so its
// digest is the content that ACTUALLY ran — captured from the trace, immutable
// once recorded, never reconstructed from disk at discover time (see
// runContentHash and SkillContentHashKey). Gated to a successful by-path load:
// a first-class skill tool's result is a launch acknowledgement (claude
// captures its body separately, from the base-directory injection), and an
// error result is not the body. A transcript-pinned store-worktree load is
// upgraded to the proven subtree hash later, in EnrichSkillIdentity.
func stampPathLoadContentHash(sp *Span, result string, isError bool) {
	if sp.Kind != KindSkill || isError || result == "" {
		return
	}
	if sp.Attributes[SkillActivationKey] != SkillActivationPath {
		return
	}
	stampRunContentHash(sp, result)
}

// stampRunContentHash sets skill.content_hash on a SKILL span from the verbatim
// skill body recorded in the transcript, unless one is already present
// (call-time evidence wins). It is the writer-side of the evolution loop's
// comparison coordinate — a RUN-TIME observation, so a run stays bucketed by
// what it ran regardless of later switch/edit/publish to the skill on disk.
func stampRunContentHash(sp *Span, body string) {
	if sp.Kind != KindSkill {
		return
	}
	if _, ok := sp.Attributes[SkillContentHashKey]; ok {
		return
	}
	if h := runContentHash(body); h != "" {
		sp.Attributes[SkillContentHashKey] = h
	}
}

// runContentHash digests the verbatim skill body the transcript captured at
// load time into the canonical "sha256:<hex>" coordinate the evolution loop
// buckets by. Line endings are normalized and surrounding whitespace trimmed so
// a trailing-newline or CRLF difference in how the body was recorded can't split
// one version into two cohorts. An empty body yields "" (nothing to stamp).
//
// This is deliberately a digest of the recorded BYTES, not the canonical subtree
// hash: the subtree hash needs the whole on-disk tree (which discover may read
// after the skill was switched/edited), whereas the body is in the trace and
// fixed forever. The two schemes therefore differ — content_hash is purely a
// comparison key, never an attestation (see SkillContentHashKey).
func runContentHash(body string) string {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RunContentHash exposes the body-digest coordinate (see SkillContentHashKey) so
// a display-time caller can recompute it for an on-disk SKILL.md body and
// corroborate which recorded cohort is the version installed right now. It uses
// the identical normalization as the deriver, so a hash computed here over the
// current install's body matches the coordinate stamped on a run that loaded the
// same bytes.
func RunContentHash(body string) string { return runContentHash(body) }

// mineSkillLoadPath extracts load-path evidence for a SKILL span from a tool
// result's text. Gated on the span's own skill.name so a result that mentions
// other skills' paths can never mis-attribute; the path is recorded only when
// the span has none yet (call-time evidence wins).
func mineSkillLoadPath(sp *Span, text string) {
	if sp.Kind != KindSkill || text == "" {
		return
	}
	if _, ok := sp.Attributes["skill.load_path"]; ok {
		return
	}
	name, _ := sp.Attributes["skill.name"].(string)
	if name == "" {
		return
	}
	// The worktree-path branch of pathSkillRef ignores the valid set, so the
	// returned name must be re-checked against the span's skill.
	if n, _, path := pathSkillRef(text, map[string]bool{name: true}); n == name && path != "" {
		// Some results spell the location as a file:// URI (opencode's
		// base-directory line); enrichment resolves filesystem paths.
		sp.Attributes["skill.load_path"] = strings.TrimPrefix(path, "file://")
	}
}

// attachSkillLoadPath sets skill.load_path on the turn's most recent SKILL
// span that lacks one — for agents whose load-path evidence arrives in a
// separate record after the skill invocation (claude's base-directory
// injection, copilot's skill.invoked event). A non-empty name additionally
// requires the span's skill.name to match. It reports whether a pending span
// was found and stamped, so a caller can fall back to creating one when the
// load had no preceding tool call (claude's harness-injected skills).
func (t *turn) attachSkillLoadPath(name, path string) bool {
	if path == "" {
		return false
	}
	for i := len(t.tools) - 1; i >= 0; i-- {
		sp := &t.tools[i]
		if sp.Kind != KindSkill {
			continue
		}
		if name != "" {
			if n, _ := sp.Attributes["skill.name"].(string); n != name {
				continue
			}
		}
		if _, ok := sp.Attributes["skill.load_path"]; ok {
			continue
		}
		sp.Attributes["skill.load_path"] = path
		// A post-invocation load record is positive evidence the skill loaded, so
		// the span votes success — unless a result already gave it a real outcome
		// (e.g. an interrupt). Observed evidence, never a fabricated default: only
		// a span we actually saw load gets it. This is what lets a tool-less
		// advisory skill (claude delivers its load via this injection, not a
		// tool_result) roll up to success instead of unknown.
		if _, ok := sp.Attributes[OutcomeKey]; !ok {
			sp.Attributes[OutcomeKey] = OutcomeSuccess
		}
		return true
	}
	return false
}

// attachSkillBody stamps the run-time content coordinate (skill.content_hash,
// the digest of the verbatim SKILL.md body) on the turn's most recent SKILL
// span that lacks one — the companion of attachSkillLoadPath for agents whose
// skill body arrives in a separate record after the invocation (claude's
// base-directory injection). A non-empty name additionally requires the span's
// skill.name to match, so parallel skill loads in one turn each digest their own
// body.
func (t *turn) attachSkillBody(name, body string) {
	if body == "" {
		return
	}
	for i := len(t.tools) - 1; i >= 0; i-- {
		sp := &t.tools[i]
		if sp.Kind != KindSkill {
			continue
		}
		if name != "" {
			if n, _ := sp.Attributes["skill.name"].(string); n != name {
				continue
			}
		}
		if _, ok := sp.Attributes[SkillContentHashKey]; ok {
			continue
		}
		stampRunContentHash(sp, body)
		return
	}
}

// turnWalk is the shared state machine the JSONL derivers drive: it owns the
// open turn, the running index, and the accumulated spans, so a deriver only
// translates its record shapes into openTurn/prompt/output/tool calls.
//
// The display fields (agentLabel/rootName/integration/runDepth/agentType) shape
// the clean span tree every deriver shares: each turn emits a root CHAIN span
// (rootName, e.g. "Claude Code Turn") that parents the model span (agentLabel,
// e.g. "Claude") and its tool/skill children. They carry no semantics a query
// depends on (kind + gen_ai.*/skill.* attributes do that); they exist so the
// trace reads cleanly. A deriver sets them once in Derive; subagent derivation
// sets runDepth>=1 and agentType="subagent".
type turnWalk struct {
	sessionID   string
	idSalt      string // namespaces deterministic ids for a nested walk (subagent agentId); "" for the main walk
	agentLabel  string // model-span display name ("Claude", "Codex")
	rootName    string // root turn-span display name ("Claude Code Turn")
	integration string // qvr.integration tag ("claude-code", "codex")
	runDepth    int    // 0 = main session, >=1 = nested subagent
	agentType   string // "agent" (default) | "subagent"
	spans       []Span
	cur         *turn
	turnIdx     int
	model       string // most recent model seen; stamped on new turns
}

// open starts a fresh turn at ts (flushing any open one).
func (w *turnWalk) open(ts int64) {
	w.flush()
	w.turnIdx++
	// idSalt namespaces a nested walk's ids (a subagent's agentId) so its trace
	// and span ids never collide with the main walk's; it stays absent for the
	// main walk, keeping those ids unchanged.
	idParts := []string{w.sessionID}
	if w.idSalt != "" {
		idParts = append(idParts, w.idSalt)
	}
	idParts = append(idParts, "turn", strconv.Itoa(w.turnIdx))
	tid := traceID(idParts...)
	w.cur = &turn{
		index:      w.turnIdx,
		startMs:    ts,
		endMs:      ts,
		model:      w.model,
		traceID:    tid,
		rootSpanID: spanID(tid, "turn"),
		llmSpanID:  spanID(tid, "llm"),
		pending:    map[string]int{},
	}
}

// ensure opens a turn at ts when none is currently open (e.g. a session
// resumed mid-turn) so nothing is dropped.
func (w *turnWalk) ensure(ts int64) {
	if w.cur == nil {
		w.open(ts)
	}
}

// setModel records the model for the current and subsequent turns.
func (w *turnWalk) setModel(model string) {
	if model == "" {
		return
	}
	w.model = model
	if w.cur != nil {
		w.cur.model = model
	}
}

// flush emits the open turn's root → LLM → tool/skill spans and clears it. The
// root CHAIN span wraps the turn (it is what a subagent tree hangs under, and
// what carries the turn-level Input/Output and trace metadata); the model span
// and tool/skill spans nest beneath it.
func (w *turnWalk) flush() {
	if w.cur == nil {
		return
	}
	w.spans = append(w.spans, w.cur.rootSpan(w))
	w.spans = append(w.spans, w.cur.llmSpan(w))
	w.spans = append(w.spans, w.cur.tools...)
	w.cur = nil
}

// systemReminderRe strips the leading harness-injected reminder block some
// agents prepend to the user's first prompt.
var systemReminderRe = regexp.MustCompile(`(?is)^\s*<system-reminder>.*?</system-reminder>\s*`)

// stripSystemReminder removes a leading <system-reminder> block from a prompt.
func stripSystemReminder(s string) string {
	return systemReminderRe.ReplaceAllString(s, "")
}

// compactJSON renders any decoded JSON value back to a compact string for the
// gen_ai.tool.call.arguments attribute.
func compactJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// commandFromArgs pulls the shell command out of an exec/shell tool's
// arguments. Agents name the field "cmd" or "command"; the value is a string
// or a []string argv.
func commandFromArgs(args map[string]any) string {
	for _, k := range []string{"cmd", "command"} {
		switch v := args[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			parts := make([]string, 0, len(v))
			for _, e := range v {
				if s, ok := e.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

// turn accumulates one user→assistant exchange while we walk the transcript.
type turn struct {
	index     int
	startMs   int64
	endMs     int64
	prompt    string
	output    string
	model     string
	inTokens  int
	outTokens int
	// Cache sub-splits of inTokens (which stays the TOTAL including cache).
	// The seen flags keep absence distinct from zero: a usage record the
	// native store never wrote must derive to NO gen_ai.usage.* attributes
	// (rendered n/a downstream), never a fabricated 0. in/out are flagged
	// separately because some stores report only one side per turn
	// (copilot's per-message outputTokens).
	cacheReadTokens     int
	cacheCreationTokens int
	inSeen              bool
	outSeen             bool
	cacheReadSeen       bool
	cacheCreationSeen   bool
	reasoning           string // accumulated model thinking/reasoning text (when not encrypted)
	traceID             string
	rootSpanID          string // the turn's root CHAIN span (parent of llmSpan)
	llmSpanID           string
	tools               []Span         // TOOL + SKILL children, parented to llmSpanID
	pending             map[string]int // tool_use_id → index into tools (awaiting result)
}

// appendReasoning accumulates model thinking/reasoning text, newline-separated.
// Empty / encrypted blocks (claude's signature-only thinking, codex's
// encrypted_content with no summary text) contribute nothing, so the attribute
// stays absent rather than carrying opaque bytes.
func (t *turn) appendReasoning(s string) {
	if s == "" {
		return
	}
	if t.reasoning != "" {
		t.reasoning += "\n"
	}
	t.reasoning += s
}

// addUsage folds one native usage record into the turn. in is the TOTAL input
// (cache included — claude and copilot report it that way natively; derivers
// for formats that split it out sum before calling).
func (t *turn) addUsage(in, out int) {
	t.inSeen = true
	t.outSeen = true
	t.inTokens += in
	t.outTokens += out
}

// addOutputUsage records output tokens for a format that reports only the
// output side per turn; the input side stays absent (n/a), never 0.
func (t *turn) addOutputUsage(out int) {
	t.outSeen = true
	t.outTokens += out
}

// addCacheRead records cache-read input tokens, a sub-split of the input
// total, only meaningful when the format reports the split.
func (t *turn) addCacheRead(n int) {
	t.cacheReadSeen = true
	t.cacheReadTokens += n
}

// addCacheCreation records cache-write input tokens (claude's cache_creation,
// copilot/opencode's cache write), a sub-split of the input total.
func (t *turn) addCacheCreation(n int) {
	t.cacheCreationSeen = true
	t.cacheCreationTokens += n
}

// turnMessages renders the turn's input (the user prompt) and output (the final
// assistant text) as the JSON message arrays the gen_ai.* convention uses; both
// the root and the model span carry them so each reads cleanly on its own.
func (t *turn) turnMessages() (in, out string) {
	output := t.output
	if output == "" {
		output = "(no text output)"
	}
	inB, _ := json.Marshal([]map[string]string{{"role": "user", "content": t.prompt}})
	outB, _ := json.Marshal([]map[string]string{{"role": "assistant", "content": output}})
	return string(inB), string(outB)
}

// rootSpan renders the turn's root CHAIN span: the clean tree's top node for the
// turn. It carries the turn-level Input/Output and the trace metadata
// (qvr.thread_id/integration/run_depth/agent_type) so a consumer can tag the
// turn back to its session and nesting depth without walking children. It is
// the parent the model span and (via subagent linkage) a nested subagent tree
// hang under.
func (t *turn) rootSpan(w *turnWalk) Span {
	in, out := t.turnMessages()
	end := max(t.endMs, t.startMs)
	agentType := w.agentType
	if agentType == "" {
		agentType = "agent"
	}
	attrs := map[string]any{
		"session.id":             w.sessionID,
		"gen_ai.operation.name":  "chain",
		"gen_ai.input.messages":  in,
		"gen_ai.output.messages": out,
		"qvr.thread_id":          w.sessionID,
		"qvr.run_depth":          w.runDepth,
		"qvr.agent_type":         agentType,
	}
	if w.integration != "" {
		attrs["qvr.integration"] = w.integration
	}
	name := w.rootName
	if name == "" {
		name = "Turn"
	}
	return Span{
		Name:       name,
		Kind:       KindChain,
		SpanID:     t.rootSpanID,
		TraceID:    t.traceID,
		StartMs:    t.startMs,
		EndMs:      end,
		Attributes: attrs,
	}
}

// llmSpan renders the turn's model span — an OTel GenAI "chat" inference span,
// nested under the turn's root. Token attributes are emitted only when the
// native store actually reported usage for the turn (the in/out seen flags); an
// unconditional 0 would be indistinguishable from a real zero and poison every
// cross-agent comparison built on these spans. Cache sub-splits ride along when
// the format distinguishes them; reasoning text rides along when it was captured
// (not encrypted).
func (t *turn) llmSpan(w *turnWalk) Span {
	in, out := t.turnMessages()
	end := max(t.endMs, t.startMs)
	name := w.agentLabel
	if name == "" {
		name = "chat"
		if t.model != "" {
			name = "chat " + t.model
		}
	}
	attrs := map[string]any{
		"session.id":             w.sessionID,
		"gen_ai.operation.name":  "chat",
		"gen_ai.request.model":   t.model,
		"gen_ai.input.messages":  in,
		"gen_ai.output.messages": out,
	}
	if t.reasoning != "" {
		attrs["qvr.reasoning"] = t.reasoning
	}
	if t.inSeen {
		attrs["gen_ai.usage.input_tokens"] = t.inTokens
		if t.cacheReadSeen {
			attrs["gen_ai.usage.cache_read_input_tokens"] = t.cacheReadTokens
		}
		if t.cacheCreationSeen {
			attrs["gen_ai.usage.cache_creation_input_tokens"] = t.cacheCreationTokens
		}
	}
	if t.outSeen {
		attrs["gen_ai.usage.output_tokens"] = t.outTokens
	}
	if p := providerName(t.model); p != "" {
		attrs["gen_ai.provider.name"] = p
	}
	return Span{
		Name:         name,
		Kind:         KindLLM,
		SpanID:       t.llmSpanID,
		TraceID:      t.traceID,
		ParentSpanID: t.rootSpanID,
		StartMs:      t.startMs,
		EndMs:        end,
		Attributes:   attrs,
	}
}

// appendOutput accumulates assistant text, newline-separated.
func (t *turn) appendOutput(s string) {
	if t.output != "" {
		t.output += "\n"
	}
	t.output += s
}

// bump extends the turn's end time to ts when ts is later.
func (t *turn) bump(ts int64) {
	if ts > t.endMs {
		t.endMs = ts
	}
}

// normType lowercases and strips separators so role/type spellings that vary
// across agents ("toolCall" / "tool_call" / "toolcall") compare equal.
func normType(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == '.' {
			return -1
		}
		return r
	}, strings.ToLower(s))
}
