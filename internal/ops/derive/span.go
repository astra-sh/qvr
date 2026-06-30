// Package derive turns raw, verbatim trace rows (see internal/ops/rawtrace)
// into OpenTelemetry spans. Raw stays the canonical source of truth; spans are
// a PROJECTION — re-running a deriver over the same raw rows yields the same
// spans, so this layer can be regenerated and evolved without ever re-capturing.
//
// The wire shape is the OTLP JSON envelope (resourceSpans → scopeSpans →
// spans). Attributes follow OpenTelemetry's GenAI semantic conventions
// (gen_ai.*: see https://opentelemetry.io/docs/concepts/signals/traces/), plus
// the Quiver skill.* extension family tagging which skill a span belongs to:
// skill.name (set by the derivers) and, when resolved from the calling
// project's qvr.lock by EnrichSkillIdentity, skill.registry/version/commit/
// source/subtree_hash/canonical (see enrich.go). It is vendor-neutral: no
// backend names, no project injection. Emitting to any OTLP consumer is a
// separate, optional step.
package derive

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Default resource/scope identifiers stamped on emitted spans. Vendor-neutral.
const (
	ServiceName = "quiver"
	ScopeName   = "quiver-trace"
)

// Span categories. These are Quiver's own labels (stored in the spans.kind
// column and shown in `qvr audit logs`); the emitted OTLP span carries the
// equivalent semantics via OTel gen_ai.* attributes, not via this string.
// LLM = a model turn, TOOL = a tool call, SKILL = a skill load.
const (
	KindLLM   = "LLM"
	KindTool  = "TOOL"
	KindChain = "CHAIN"
)

// OutcomeKey is the Quiver per-span result attribute on TOOL/SKILL spans: did
// the call succeed, fail, or get blocked? It is derived purely from the
// transcript — a tool result's error flag and, for a denied/interrupted call,
// its text — so it stays a regenerable projection. Its ABSENCE means unknown
// (the call never reported a result); we never fabricate a success. The session
// read model rolls these up into SessionMeta.Outcome (worst-of-spans).
const OutcomeKey = "qvr.outcome"

// The three outcome values a span can carry. "Unknown" is NOT a value — it is
// the ABSENCE of the attribute (and the empty session rollup), so every
// comparison site checks against "" rather than a sentinel string.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeBlocked = "blocked"
)

// SkillActivationKey records HOW a SKILL span's skill was identified, so a
// consumer can tell a genuine activation from a file-touch. A first-class skill
// tool call ("tool") is the agent actually loading the skill; a path signal
// scraped from a command or argument that merely referenced the skill's files
// ("path") is not. Both currently lift to a SKILL span, which over-counts
// activations ~2× for path-signal agents (codex); the evolution loop filters to
// "tool" to count real activations. Absence means the span predates this tag.
const SkillActivationKey = "skill.activation"

// The skill.activation values — how a SKILL span's load was identified, weakest
// last. A consumer can down-weight or exclude the softer signals.
const (
	SkillActivationTool = "tool"
	SkillActivationPath = "path"
	// SkillActivationInjection is set when the harness loaded the skill directly
	// (claude's "Base directory for this skill:" injection) with no preceding
	// Skill tool call — a genuine load, just delivered by injection, not a tool.
	SkillActivationInjection = "injection"
	// SkillActivationImplicit is set when the skill was attributed from the
	// agent's own narration ("Using the `X` skill"), gated to the session's
	// announced catalog, when no tool call or file read evidenced the load.
	SkillActivationImplicit = "implicit"
)

// SkillContentHashKey is the digest of the skill content that ACTUALLY ran,
// fixed by the RUN-TIME bytes the transcript captured — never reconstructed
// from disk at discover time, so a run stays bucketed by what it ran even after
// the skill is switched, edited, or republished. It is the evolution loop's
// comparison coordinate: it changes exactly when the skill's body changes (what
// `qvr edit` does), making before/after cohorts comparable without trusting an
// un-normalized ref.
//
// It is fed in a strict precedence, most run-immutable first (see
// derive.runContentHash, enrich.stampContentHash and
// enrich.stampSnapshotContentHash):
//
//  1. transcript-pinned — the recorded load path is a store-worktree path (the
//     sha is in the captured bytes), so the proof-gated ATTESTATION
//     skill.subtree_hash, the canonical full-subtree digest, is the coordinate.
//     Preferred over a body digest here because a transcript may have captured
//     only a PARTIAL read of the body.
//  2. run-time body — the sha256 digest of the verbatim SKILL.md body the agent
//     loaded, taken straight from the trace. Fixed by the exact bytes the run
//     loaded, so it survives any later switch/edit/publish. This is what a
//     symlink-recorded claude load uses.
//  3. snapshot subtree (last resort) — when a load captured NO body and is
//     proven only by the session's ingest-time snapshot, the snapshot's frozen
//     subtree_hash. It ranks BELOW the body digest: the snapshot froze at first
//     ingest, which can post-date a switch, so it must never override the exact
//     body a run actually loaded.
//
// A plain body digest is an OBSERVATION, never an attestation: it is a
// comparison key only, and its scheme differs from subtree_hash (a body, not
// the tree), so two agents cohort together only when they leave the same kind
// of run-immutable evidence.
const SkillContentHashKey = "skill.content_hash"

// classifyOutcome maps a tool result to an outcome. A user denial or interrupt
// is blocked (a governance signal, not a tool defect); any other error is a
// failure; otherwise the call succeeded.
//
// The blocked check runs BEFORE the error-flag gate on purpose: a denial /
// interrupt is echoed into the result text by specific marker phrases, and some
// harnesses surface it WITHOUT setting the error flag (observed on Claude skill
// loads, whose tool_result can carry "[request interrupted…" with is_error
// unset). Gating it on isError would miscount those as success. The markers are
// precise governance phrases, not generic "error" words, so a successful result
// is never falsely demoted — we still trust the error flag for tool defects and
// never fabricate a failure from arbitrary text.
func classifyOutcome(result string, isError bool) string {
	if isBlockedResult(result) {
		return OutcomeBlocked
	}
	if !isError {
		return OutcomeSuccess
	}
	return OutcomeFailure
}

// blockedResultMarkers are substrings a harness writes into a tool result when
// the user REJECTED or INTERRUPTED the call rather than the tool failing on its
// own (observed in real Claude Code stores). Matched case-insensitively. This
// is the only "blocked" source available to a pure deriver: hook-deny decisions
// live in hook payloads, which the deriver never sees (capture filters to
// transcript rows), so denial that a transcript doesn't echo derives to failure.
var blockedResultMarkers = []string{
	"the user doesn't want to proceed",
	"tool use was rejected",
	"user doesn't want to take this action",
	"request interrupted by user",
	"[request interrupted",
}

func isBlockedResult(result string) bool {
	if result == "" {
		return false
	}
	low := strings.ToLower(result)
	for _, m := range blockedResultMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// Span is one derived span. Times are epoch milliseconds. Attributes use OTel
// GenAI semantic-convention keys (e.g. "gen_ai.operation.name",
// "gen_ai.request.model", "gen_ai.usage.input_tokens") plus the Quiver
// "skill.*" extension family (see the package doc). It is a plain struct so
// callers can inspect or re-serialize it; ToOTLP renders the wire form.
type Span struct {
	Name         string
	Kind         string
	SpanID       string
	TraceID      string
	ParentSpanID string
	StartMs      int64
	EndMs        int64
	Attributes   map[string]any
}

// spanKindInt maps a Quiver category to the OTLP SpanKind integer. Derived
// spans are reconstructed, not live RPCs, so they are all INTERNAL(1). The
// named cases let a caller request a standard kind explicitly if needed.
func spanKindInt(kind string) int {
	switch kind {
	case "SERVER":
		return 2
	case "CLIENT":
		return 3
	case "PRODUCER":
		return 4
	case "CONSUMER":
		return 5
	case "UNSPECIFIED":
		return 0
	default: // LLM, TOOL, SKILL, CHAIN, INTERNAL, ""
		return 1
	}
}

// otlpAttrValue converts a Go value to an OTLP attribute value (bool before
// int; whole floats become intValue).
func otlpAttrValue(v any) map[string]any {
	switch t := v.(type) {
	case bool:
		return map[string]any{"boolValue": t}
	case int:
		return map[string]any{"intValue": strconv.Itoa(t)}
	case int64:
		return map[string]any{"intValue": strconv.FormatInt(t, 10)}
	case float64:
		if t == float64(int64(t)) {
			return map[string]any{"intValue": strconv.FormatInt(int64(t), 10)}
		}
		return map[string]any{"doubleValue": t}
	case string:
		return map[string]any{"stringValue": t}
	default:
		b, _ := json.Marshal(t)
		return map[string]any{"stringValue": string(b)}
	}
}

func msToNano(ms int64) string {
	return strconv.FormatInt(ms, 10) + "000000"
}

// otlpSpan renders the inner span object (the element of scopeSpans[].spans).
func (s Span) otlpSpan() map[string]any {
	attrs := make([]map[string]any, 0, len(s.Attributes))
	for k, v := range s.Attributes {
		attrs = append(attrs, map[string]any{"key": k, "value": otlpAttrValue(v)})
	}
	end := s.EndMs
	if end == 0 {
		end = s.StartMs
	}
	obj := map[string]any{
		"traceId":           s.TraceID,
		"spanId":            s.SpanID,
		"name":              s.Name,
		"kind":              spanKindInt(s.Kind),
		"startTimeUnixNano": msToNano(s.StartMs),
		"endTimeUnixNano":   msToNano(end),
		"attributes":        attrs,
		"status":            map[string]any{"code": otlpStatusCode(s.Attributes)},
	}
	if s.ParentSpanID != "" {
		obj["parentSpanId"] = s.ParentSpanID
	}
	return obj
}

// otlpStatusCode maps a span's outcome to an OTLP status code: a failed or
// blocked call is ERROR(2), an explicit success is OK(1), and anything without
// an outcome (LLM turns, calls whose result was never seen) is UNSET(0), the
// OTel default. Before this, every span emitted OK(1) unconditionally — a
// failed tool call looked successful to any OTLP backend.
func otlpStatusCode(attrs map[string]any) int {
	switch attrs[OutcomeKey] {
	case OutcomeFailure, OutcomeBlocked:
		return 2
	case OutcomeSuccess:
		return 1
	default:
		return 0
	}
}

// ToOTLP renders a batch of spans as a single OTLP resourceSpans payload. For
// an empty batch it returns a well-formed envelope with an empty resourceSpans
// array — never a nil/`null` body — so a consumer always receives valid OTLP
// (an underivable session POSTs "no spans", not garbage).
func ToOTLP(spans []Span) map[string]any {
	if len(spans) == 0 {
		return map[string]any{"resourceSpans": []map[string]any{}}
	}
	inner := make([]map[string]any, 0, len(spans))
	for _, sp := range spans {
		inner = append(inner, sp.otlpSpan())
	}
	return map[string]any{
		"resourceSpans": []map[string]any{{
			"resource": map[string]any{
				"attributes": []map[string]any{
					{"key": "service.name", "value": otlpAttrValue(ServiceName)},
				},
			},
			"scopeSpans": []map[string]any{{
				"scope": map[string]any{"name": ScopeName},
				"spans": inner,
			}},
		}},
	}
}
