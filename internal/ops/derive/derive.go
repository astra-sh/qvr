package derive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/google/uuid"
)

// Version is the deriver revision stamped on every persisted span. Bump it when
// the derivation logic changes meaningfully, so stored spans from an older
// deriver can be told apart and re-derived for parity comparison.
//
// v2: OpenTelemetry gen_ai.* semantic conventions (was OpenInference in v1).
// v3: skill.* identity (registry/version/commit/source/subtree_hash/canonical)
// resolved from qvr.lock via EnrichSkillIdentity (#146).
// v4: load-path-aware attribution — codex spans carry skill.load_path, and
// EnrichSkillIdentity asserts lock identity only when the loaded path proves it
// (stamping skill.verified); unprovable identity is withheld or flagged (#149).
// v5: unified session model — every derivation also constructs a SessionMeta
// (the one read model the UI/CLI/metrics list sessions from), and agents are
// keyed by canonical target name.
// v6: proof-gated identity — skill.verified is gone; identity fields are
// stamped only on load-path proof, and skill.version's presence IS the
// verified signal (always set on proof, short SHA when the entry has no ref).
// claude gains load paths: the harness-injected isMeta "Base directory for
// this skill:" line (observed in real session stores, 2026-06-11) plus the
// universal path signal over tool calls; isMeta lines no longer fabricate
// turns.
// v7: honest token accounting — gen_ai.usage.* attributes are emitted only
// when the native store reported usage (absence ≠ 0, so token-less agents
// derive to n/a, never a fabricated zero); cache sub-splits
// (gen_ai.usage.cache_read_input_tokens / cache_creation_input_tokens) where
// the format distinguishes them; SessionMeta carries session token totals
// (from spans, or natively for stores that report only session-level usage).
// claude usage dedupes by message id (per-block lines repeat the same id and
// usage — summing per line inflated tokens ~2×), and gemini's polymorphic
// resultDisplay (string OR object) no longer drops the whole item. Dispatch
// is also mixed-agent safe: the majority transcript agent owns the session,
// so a stale external writer's foreign rows can't blank it.
// v8: outcome signal — TOOL/SKILL spans carry qvr.outcome
// (success/failure/blocked; absent = unknown), derived from the result's error
// flag plus a denial/interrupt marker for blocked (hook-deny lives in hook
// payloads the deriver never sees, so transcript-silent denial is failure).
// SessionMeta carries a worst-of-spans rollup (Outcome), and the OTLP span
// status code now reflects it (ERROR for failure/blocked) instead of a
// hardcoded OK. Rederive backfills outcome onto historical sessions.
// v9: a skill LOAD votes too — a SKILL span carries qvr.outcome=success once its
// load is positively observed (its result, or the post-invocation load record:
// claude's base-directory injection / copilot's skill.invoked). This is observed
// evidence, never a fabricated default — only a span we saw load gets it — and a
// real error/interrupt result still wins. It lets a tool-less advisory/triage
// skill roll up to success instead of unknown (the flagship eval leads with
// `outcome: success`). Rederive backfills it onto historical sessions.
// v10: run-immutable content coordinate — skill.content_hash is fixed by the
// bytes the transcript captured, never re-read from disk at discover (which
// re-bound any run found after a switch/edit/publish to the current version).
// It is the digest of the verbatim skill body the run loaded (claude's
// base-directory injection minus its per-call "ARGUMENTS:" trailer, or a by-path
// read's result body), upgraded to the proven subtree hash only when the
// recorded load path itself pins the version sha. Rederive re-coordinates
// historical sessions; the disk-walk hashing is gone.
// v11: clean span tree — each turn emits a root CHAIN span (e.g. "Claude Code
// Turn") that parents the model span (named for the agent, "Claude"/"Codex")
// and its tool/skill children; child titles are the short tool name (MCP tools
// shown as their action, not the mcp__server__ prefix) or the skill name, with
// the exact tool name kept on gen_ai.tool.name. The root carries the turn
// Input/Output and trace metadata (qvr.thread_id / qvr.integration /
// qvr.run_depth / qvr.agent_type); model thinking/reasoning rides on the model
// span as qvr.reasoning when the transcript captured it unencrypted. Rederive
// re-projects historical sessions into the new tree.
const Version = 12

// KindSkill is the Quiver span category for a skill load/invocation within a
// turn. It exists so the trace makes skill usage a first-class, queryable stage
// (the basis for skill attribution and evals) rather than burying it inside a
// generic tool span. On the wire it is an OTel execute_tool span carrying the
// skill.name extension attribute.
const KindSkill = "SKILL"

// SessionMeta is the unified session model — the per-session header every
// consumer (UI, CLI, metrics) reads. Derivers construct it from the session's
// verbatim raw rows, so like spans it is a deterministic, regenerable
// projection: same rows + same Version ⇒ same meta.
//
// A deriver only fills the fields that need format-specific parsing (e.g.
// GitBranch); finalizeMeta fills everything derivable generically from the
// rows and spans (identity, title, model, counts, time bounds).
type SessionMeta struct {
	SessionID       uuid.UUID // canonical correlation key (same as raw_traces)
	Agent           string    // canonical target name (model.CanonicalTarget)
	SourceSessionID string    // the agent's own session id, verbatim
	SourcePath      string    // native store file the session came from
	WorkingDir      string    // cwd (project scoping)
	GitBranch       string
	Model           string // LLM model (last seen across the session)
	Title           string // first real user prompt, one line, clipped
	StartedMs       int64  // epoch ms of the first span
	EndedMs         int64  // epoch ms of the last span end
	Turns           int64  // LLM span count
	Tools           int64  // TOOL span count
	Skills          []string
	// Session token totals. nil = the native store reported no usage on that
	// side (rendered n/a, never 0). Filled from the spans' gen_ai.usage.*
	// attributes; a deriver sets them directly when its store reports usage
	// only at session level (hermes, copilot), and a deriver-set value wins.
	TokensIn  *int64
	TokensOut *int64
	// Outcome is the session-level rollup of its TOOL/SKILL spans' qvr.outcome:
	// the worst seen (failure > blocked > success), or "" when no span carried an
	// outcome (rendered unknown, never a fabricated success).
	Outcome string
}

// Derivation is one session's full derived projection: the unified meta plus
// the Turn→Tool/Skill spans. Both come from the same walk over the same rows,
// so they always describe the same derivation.
type Derivation struct {
	Meta  SessionMeta
	Spans []Span
}

// Deriver turns one session's raw rows into its derived projection.
// Implementations are per-agent because each agent's transcript format
// differs; the registry maps canonical target name → Deriver. A deriver must
// be PURE: same rows in → same Derivation out (including span ids), so the
// projection is reproducible.
type Deriver interface {
	Derive(rows []*ops.RawTrace) (*Derivation, error)
}

var registry = map[string]Deriver{}

// Register installs a deriver for an agent, keyed by canonical target name.
// Called from deriver init().
func Register(agent string, d Deriver) { registry[canonicalAgent(agent)] = d }

// Get returns the deriver for an agent, or (nil, false). The name is
// normalized through the target registry, so aliases (and agent names stored
// by earlier qvr versions, e.g. "claude-code") resolve to the same deriver.
func Get(agent string) (Deriver, bool) {
	d, ok := registry[canonicalAgent(agent)]
	return d, ok
}

// Registered returns the canonical names of every agent with a deriver, sorted.
func Registered() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// canonicalAgent normalizes an agent name or alias to its canonical target
// name; an unknown name passes through unchanged.
func canonicalAgent(name string) string {
	if c, ok := model.CanonicalTarget(name); ok {
		return c
	}
	return name
}

// DeriveSession derives one session's projection from its rows. The session's
// agent is the DOMINANT transcript agent, not simply the first row's: a stale
// external writer can inject rows under another agent name into the same
// session id, and dispatching on the first row would run the wrong deriver
// against the wrong record shapes (observed live, 2026-06-12: a foreign
// writer's rows in a session derived it to zero spans). Rows from other
// agents are excluded from derivation; raw capture stays untouched. An empty
// slice yields nil. Returns an error only if no deriver is registered.
func DeriveSession(rows []*ops.RawTrace) (*Derivation, error) {
	rows = dominantAgentRows(rows)
	if len(rows) == 0 {
		return nil, nil
	}
	agent := rows[0].AgentName
	d, ok := Get(agent)
	if !ok {
		return nil, fmt.Errorf("derive: no deriver registered for agent %q", agent)
	}
	out, err := d.Derive(rows)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = &Derivation{}
	}
	finalizeMeta(out, rows)
	return out, nil
}

// dominantAgentRows reduces a possibly mixed-agent session to its real
// agent's rows: the agent (by canonical name, so legacy aliases collapse)
// with the most TRANSCRIPT rows wins — transcript rows are the session's
// substance; hook payloads don't vote. Ties break to the agent seen first,
// keeping the choice deterministic for a given row order. A single-agent
// session passes through unchanged.
func dominantAgentRows(rows []*ops.RawTrace) []*ops.RawTrace {
	counts := map[string]int{}
	var order []string
	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		c := canonicalAgent(r.AgentName)
		if _, seen := counts[c]; !seen {
			order = append(order, c)
		}
		counts[c]++
	}
	if len(counts) <= 1 {
		return rows
	}
	winner := order[0]
	for _, c := range order[1:] {
		if counts[c] > counts[winner] {
			winner = c
		}
	}
	out := make([]*ops.RawTrace, 0, len(rows))
	for _, r := range rows {
		if canonicalAgent(r.AgentName) == winner {
			out = append(out, r)
		}
	}
	return out
}

// finalizeMeta fills every SessionMeta field derivable generically from the
// rows and spans, leaving any format-specific value a deriver already set
// (e.g. GitBranch) untouched. Keeping this shared is what lets per-agent
// derivers stay thin.
func finalizeMeta(d *Derivation, rows []*ops.RawTrace) {
	m := &d.Meta
	m.SessionID = rows[0].SessionID
	m.Agent = canonicalAgent(rows[0].AgentName)
	fillMetaIdentity(m, rows)
	if m.Title == "" {
		m.Title = firstPromptFromSpans(d.Spans, titleDefaultMaxLen)
	}
	fillMetaFromSpans(m, d.Spans)
	fillMetaTokens(m, d.Spans)
	fillMetaOutcome(m, d.Spans)
	// Formats without in-file timestamps derive zero time bounds; fall back to
	// the capture times so the session still buckets into the right day.
	if m.StartedMs == 0 && len(rows) > 0 {
		m.StartedMs = rows[0].CapturedAt.UnixMilli()
	}
	if m.EndedMs < m.StartedMs {
		m.EndedMs = m.StartedMs
	}
}

// fillMetaIdentity recovers the session's source identity (agent session id,
// source file, cwd) from the first rows that carry each value.
func fillMetaIdentity(m *SessionMeta, rows []*ops.RawTrace) {
	for _, r := range rows {
		if m.SourceSessionID == "" {
			m.SourceSessionID = r.AgentSessionID
		}
		if m.SourcePath == "" {
			m.SourcePath = r.SourcePath
		}
		if m.WorkingDir == "" {
			m.WorkingDir = r.WorkingDirectory
		}
		if m.SourceSessionID != "" && m.SourcePath != "" && m.WorkingDir != "" {
			return
		}
	}
}

// fillMetaFromSpans accumulates the time bounds, model, turn/tool counts, and
// the distinct skill list (first-use order) from the derived spans.
func fillMetaFromSpans(m *SessionMeta, spans []Span) {
	seenSkill := map[string]bool{}
	for _, sp := range spans {
		if m.StartedMs == 0 || (sp.StartMs > 0 && sp.StartMs < m.StartedMs) {
			m.StartedMs = sp.StartMs
		}
		if end := max(sp.EndMs, sp.StartMs); end > m.EndedMs {
			m.EndedMs = end
		}
		switch sp.Kind {
		case KindLLM:
			m.Turns++
			if model, ok := sp.Attributes["gen_ai.request.model"].(string); ok && model != "" {
				m.Model = model
			}
		case KindTool:
			m.Tools++
		}
		appendDistinctAttr(sp, "skill.name", seenSkill, &m.Skills)
	}
}

// appendDistinctAttr appends sp's string attribute `key` to *dst when it is
// present, non-empty, and not yet in seen — the first-use distinct accumulation
// the skill-name and skill-version session rollups share.
func appendDistinctAttr(sp Span, key string, seen map[string]bool, dst *[]string) {
	if v, ok := sp.Attributes[key].(string); ok && v != "" && !seen[v] {
		seen[v] = true
		*dst = append(*dst, v)
	}
}

// fillMetaTokens accumulates the session token totals from the LLM spans'
// usage attributes. Each side stays nil unless ≥1 span carried it (absence ≠
// 0), and a deriver-set value wins — stores that report usage only at session
// level (hermes, copilot input) set the totals natively.
func fillMetaTokens(m *SessionMeta, spans []Span) {
	var in, out *int64
	for _, sp := range spans {
		if sp.Kind != KindLLM {
			continue
		}
		addAttrTokens(&in, sp.Attributes["gen_ai.usage.input_tokens"])
		addAttrTokens(&out, sp.Attributes["gen_ai.usage.output_tokens"])
	}
	if m.TokensIn == nil {
		m.TokensIn = in
	}
	if m.TokensOut == nil {
		m.TokensOut = out
	}
}

// DefaultOutcomeFailureThreshold is the fraction of a session's outcome-bearing
// TOOL/SKILL spans that must have FAILED before the session rolls up as a
// failure. Require >80% so a minority of errored tool calls amid an otherwise
// successful task doesn't read as a failed session (the old worst-of-spans rule
// let a single error doom the session — pollution, not signal).
const DefaultOutcomeFailureThreshold = 0.8

// outcomeFailureThreshold is the live policy value, defaulting to the constant
// above. It is process-wide (a single derivation policy, not a per-call input)
// and overridden from config via ConfigureOutcome.
var outcomeFailureThreshold = DefaultOutcomeFailureThreshold

// ConfigureOutcome sets the session-failure rollup threshold from config. A
// value outside (0,1] (including the zero value of an unset field) leaves the
// 0.8 default in place. Call it once per process before deriving — the `qvr`
// audit entry points do, so a configured ops.outcome_failure_threshold takes
// effect on the next capture / rederive.
func ConfigureOutcome(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if t := cfg.Ops.OutcomeFailureThreshold; t > 0 && t <= 1 {
		outcomeFailureThreshold = t
	}
}

// fillMetaOutcome rolls the TOOL/SKILL spans' qvr.outcome into one session-level
// verdict. It is failure only when the FAILED fraction of outcome-bearing spans
// exceeds outcomeFailureThreshold (default >80%); below that, a blocked span (a
// hard policy denial) still surfaces, else success. Spans without an outcome
// (LLM turns, calls whose result was never seen) don't vote; a session where
// none voted stays "" (unknown), never a fabricated success.
func fillMetaOutcome(m *SessionMeta, spans []Span) {
	var nSuccess, nFailure, nBlocked int
	for _, sp := range spans {
		if sp.Kind != KindTool && sp.Kind != KindSkill {
			continue
		}
		switch v, _ := sp.Attributes[OutcomeKey].(string); v {
		case OutcomeSuccess:
			nSuccess++
		case OutcomeFailure:
			nFailure++
		case OutcomeBlocked:
			nBlocked++
		}
	}
	total := nSuccess + nFailure + nBlocked
	switch {
	case total == 0:
		m.Outcome = "" // unknown — nothing voted
	case nFailure > 0 && float64(nFailure)/float64(total) > outcomeFailureThreshold:
		m.Outcome = OutcomeFailure
	case nBlocked > 0:
		m.Outcome = OutcomeBlocked
	default:
		m.Outcome = OutcomeSuccess
	}
}

// addAttrTokens folds one span's token attribute value into a running
// nullable sum; an absent attribute leaves the sum untouched (and nil).
func addAttrTokens(sum **int64, v any) {
	var n int64
	switch t := v.(type) {
	case int:
		n = int64(t)
	case int64:
		n = t
	case float64:
		n = int64(t)
	default:
		return
	}
	if *sum == nil {
		*sum = new(int64)
	}
	**sum += n
}

// --- deterministic id helpers ---
//
// Spans are a regenerable projection, so ids are derived from stable content
// (session + turn + tool) rather than randomness: re-deriving the same rows
// reproduces the same trace/span ids. Format matches the reference (32-hex
// trace id, 16-hex span id) so any OTLP consumer accepts them.

func traceID(parts ...string) string { return hashHex(16, parts...) } // 16 bytes → 32 hex
func spanID(parts ...string) string  { return hashHex(8, parts...) }  // 8 bytes  → 16 hex

func hashHex(n int, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:n])
}
