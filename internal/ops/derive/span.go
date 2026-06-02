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
		"status":            map[string]any{"code": 1},
	}
	if s.ParentSpanID != "" {
		obj["parentSpanId"] = s.ParentSpanID
	}
	return obj
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
