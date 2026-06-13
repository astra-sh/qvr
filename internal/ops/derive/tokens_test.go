package derive_test

import (
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// Token-accounting contract across derivers (deriver v7):
//   - gen_ai.usage.* attributes exist ONLY when the native store reported
//     usage — absence ≠ 0, so token-less agents derive to n/a downstream.
//   - gen_ai.usage.input_tokens is the TOTAL including cache; the
//     cache_read/cache_creation attributes are its sub-split, emitted only
//     when the format distinguishes them.
//   - SessionMeta.TokensIn/TokensOut sum the LLM spans' usage, or come
//     straight from the store for formats that report only session-level
//     totals (hermes, copilot's input side) — deriver-set values win.

// wantNoUsage asserts a span carries none of the usage attributes.
func wantNoUsage(t *testing.T, sp *derive.Span) {
	t.Helper()
	for _, k := range []string{
		"gen_ai.usage.input_tokens",
		"gen_ai.usage.output_tokens",
		"gen_ai.usage.cache_read_input_tokens",
		"gen_ai.usage.cache_creation_input_tokens",
	} {
		if v, ok := sp.Attributes[k]; ok {
			t.Errorf("%s present (%v) on a turn whose store reported no usage — fabricated data", k, v)
		}
	}
}

// wantMetaTokens asserts the unified meta's session token totals.
func wantMetaTokens(t *testing.T, m derive.SessionMeta, in, out int64) {
	t.Helper()
	if m.TokensIn == nil || *m.TokensIn != in {
		t.Errorf("meta tokens_in = %v, want %d", m.TokensIn, in)
	}
	if m.TokensOut == nil || *m.TokensOut != out {
		t.Errorf("meta tokens_out = %v, want %d", m.TokensOut, out)
	}
}

// TestClaudeDerive_CacheSplit pins the claude usage mapping: the native
// input_tokens EXCLUDES cache reads/writes, so the emitted input total folds
// them back in, with the cache attrs as the sub-split.
func TestClaudeDerive_CacheSplit(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440001")
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-12T00:00:00.000Z","message":{"role":"user","content":"hi"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-12T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8",`+
			`"usage":{"input_tokens":10,"cache_read_input_tokens":900,"cache_creation_input_tokens":90,"output_tokens":20},`+
			`"content":[{"type":"text","text":"hello"}]}}`),
	}
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, llm, map[string]any{
		"gen_ai.usage.input_tokens":                10 + 900 + 90, // total incl. cache
		"gen_ai.usage.output_tokens":               20,
		"gen_ai.usage.cache_read_input_tokens":     900,
		"gen_ai.usage.cache_creation_input_tokens": 90,
	})
	wantMetaTokens(t, d.Meta, 1000, 20)
}

// TestClaudeDerive_PerBlockUsageDedup pins the message-id dedupe: Claude Code
// writes one JSONL line per content block of an API message, repeating the
// same message.id AND the same usage object on every line (observed live,
// 2026-06-12: a finished session summed to 141,917 input per line vs the
// 71,791 message-id truth — ~2× inflation). Usage must count once per id;
// a different message id in the same turn still adds.
func TestClaudeDerive_PerBlockUsageDedup(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440005")
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-12T00:00:00.000Z","message":{"role":"user","content":"hi"}}`),
		// ONE API message (msg_01) split across two per-block lines, identical usage.
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-12T00:00:01.000Z","message":{"id":"msg_01","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":1000,"cache_read_input_tokens":9000,"output_tokens":50},"content":[{"type":"text","text":"working"}]}}`),
		row(sid, 2, `{"type":"assistant","timestamp":"2026-06-12T00:00:02.000Z","message":{"id":"msg_01","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":1000,"cache_read_input_tokens":9000,"output_tokens":50},"content":[{"type":"tool_use","id":"tu1","name":"Read","input":{"file_path":"/x"}}]}}`),
		// A SECOND API call in the same turn (tool loop): its usage adds.
		row(sid, 3, `{"type":"assistant","timestamp":"2026-06-12T00:00:03.000Z","message":{"id":"msg_02","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":500,"cache_read_input_tokens":9500,"output_tokens":25},"content":[{"type":"text","text":"done"}]}}`),
	}
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, llm, map[string]any{
		"gen_ai.usage.input_tokens":            10000 + 10000, // msg_01 once + msg_02 once
		"gen_ai.usage.output_tokens":           50 + 25,
		"gen_ai.usage.cache_read_input_tokens": 9000 + 9500,
	})
	wantMetaTokens(t, d.Meta, 20000, 75)
}

// TestClaudeDerive_NoUsage pins the absence invariant: an assistant line with
// no usage object derives a turn with NO gen_ai.usage.* keys, and the meta
// totals stay nil.
func TestClaudeDerive_NoUsage(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440002")
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-12T00:00:00.000Z","message":{"role":"user","content":"hi"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-12T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hello"}]}}`),
	}
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, _ := splitSpanKinds(d.Spans)
	wantNoUsage(t, llm)
	if d.Meta.TokensIn != nil || d.Meta.TokensOut != nil {
		t.Errorf("meta tokens = %v/%v, want nil/nil (no usage reported)", d.Meta.TokensIn, d.Meta.TokensOut)
	}
}

// TestCodexDerive_CachedInputTokens pins the codex cache sub-split: the
// rollout's cached_input_tokens (a subset of input_tokens) becomes the
// cache-read attribute; codex reports no cache-creation counter, so that
// attribute must stay absent.
func TestCodexDerive_CachedInputTokens(t *testing.T) {
	sid := uuid.MustParse("019e88f6-6dca-7c63-89d1-74e9c5f2eaca")
	rows := []*ops.RawTrace{
		codexRow(sid, 0, `{"timestamp":"2026-06-12T15:31:51.966Z","type":"event_msg","payload":{"type":"task_started"}}`),
		codexRow(sid, 1, `{"timestamp":"2026-06-12T15:31:54.008Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`),
		codexRow(sid, 2, `{"timestamp":"2026-06-12T15:32:03.618Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":2000,"cached_input_tokens":1500,"output_tokens":50}}}}`),
		codexRow(sid, 3, `{"timestamp":"2026-06-12T15:32:06.386Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":2420,"cached_input_tokens":2000,"output_tokens":63}}}}`),
		codexRow(sid, 4, `{"timestamp":"2026-06-12T15:32:06.415Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`),
	}
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, llm, map[string]any{
		"gen_ai.usage.input_tokens":            2000 + 2420, // native total already includes cache
		"gen_ai.usage.output_tokens":           50 + 63,
		"gen_ai.usage.cache_read_input_tokens": 1500 + 2000,
	})
	if _, ok := llm.Attributes["gen_ai.usage.cache_creation_input_tokens"]; ok {
		t.Error("cache_creation_input_tokens present — codex never reports it")
	}
	wantMetaTokens(t, d.Meta, 4420, 113)
}

// TestGeminiDerive_Tokens pins the gemini usage mapping (observed live,
// 2026-06-12): per-item tokens {input, output, cached, thoughts, tool} where
// cached ⊆ input and thoughts bill output-side — and the store re-emits an
// item as it grows (same id, same tokens, more content), so usage counts once
// per item id.
func TestGeminiDerive_Tokens(t *testing.T) {
	rows := agentRows("gemini",
		`{"id":"u1","type":"user","timestamp":"2026-06-12T10:00:00.000Z","content":[{"text":"hi"}]}`,
		`{"id":"g1","type":"gemini","model":"gemini-3-pro","timestamp":"2026-06-12T10:00:01.000Z","content":[{"text":"part"}],"tokens":{"input":9870,"output":21,"cached":7647,"thoughts":155,"tool":0,"total":10046}}`,
		// The grown re-emission of the same item: identical id and tokens.
		`{"id":"g1","type":"gemini","model":"gemini-3-pro","timestamp":"2026-06-12T10:00:02.000Z","content":[{"text":"part grown"}],"tokens":{"input":9870,"output":21,"cached":7647,"thoughts":155,"tool":0,"total":10046}}`,
	)
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, llm, map[string]any{
		"gen_ai.usage.input_tokens":            9870,     // cached is a subset; counted ONCE despite re-emission
		"gen_ai.usage.output_tokens":           21 + 155, // thoughts bill output-side
		"gen_ai.usage.cache_read_input_tokens": 7647,
	})
	wantMetaTokens(t, d.Meta, 9870, 176)
}

// TestGeminiDerive_ObjectResultDisplay pins the polymorphic resultDisplay
// handling (observed live, 2026-06-12): errors carry a string, but a
// successful call carries an OBJECT ({summary, files[]}). A string-typed
// field failed the whole item's unmarshal, silently dropping the tool span
// and the item's tokens with it.
func TestGeminiDerive_ObjectResultDisplay(t *testing.T) {
	rows := agentRows("gemini",
		`{"id":"u1","type":"user","timestamp":"2026-06-12T10:00:00.000Z","content":[{"text":"list the skill dir"}]}`,
		`{"id":"g1","type":"gemini","model":"gemini-3-pro","timestamp":"2026-06-12T10:00:01.000Z","content":[{"text":"listing"}],`+
			`"toolCalls":[{"name":"list_directory","id":"tc1","args":{"path":".agents/skills/code-review"},`+
			`"resultDisplay":{"summary":"Found 3 item(s).","files":["SKILL.md"]}}],`+
			`"tokens":{"input":9000,"output":50,"cached":0,"thoughts":100,"tool":0,"total":9150}}`,
	)
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, skill := splitSpanKinds(d.Spans)
	if skill == nil {
		t.Fatalf("tool call with object resultDisplay was dropped: %d spans", len(d.Spans))
	}
	if res, _ := skill.Attributes["gen_ai.tool.call.result"].(string); !strings.Contains(res, "Found 3 item(s).") {
		t.Errorf("object resultDisplay not rendered onto the span: %q", res)
	}
	// The item's tokens must survive too — the silent drop ate them before.
	wantAttrs(t, llm, map[string]any{
		"gen_ai.usage.input_tokens":  9000,
		"gen_ai.usage.output_tokens": 150,
	})
}

// TestCopilotDerive_Tokens pins the copilot mapping (observed live,
// 2026-06-12): per-message data carries ONLY outputTokens (the turn's input
// side stays absent — one-sided honesty), while session.shutdown's
// modelMetrics usage rollup supplies the session totals, input included.
func TestCopilotDerive_Tokens(t *testing.T) {
	rows := agentRows("copilot",
		`{"type":"user.message","timestamp":"2026-06-12T10:00:00.000Z","data":{"content":"hi"}}`,
		`{"type":"assistant.message","timestamp":"2026-06-12T10:00:01.000Z","data":{"content":"hello","outputTokens":90}}`,
		`{"type":"session.shutdown","timestamp":"2026-06-12T10:00:02.000Z","data":{"modelMetrics":{"claude-haiku-4.5":{"usage":{"inputTokens":462522,"outputTokens":4246,"cacheReadTokens":448462,"cacheWriteTokens":13583,"reasoningTokens":1727}}}}}`,
	)
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, _ := splitSpanKinds(d.Spans)
	if v, ok := llm.Attributes["gen_ai.usage.output_tokens"]; !ok || v != 90 {
		t.Errorf("output_tokens = %v, want 90", v)
	}
	if v, ok := llm.Attributes["gen_ai.usage.input_tokens"]; ok {
		t.Errorf("input_tokens present (%v) — copilot never reports it per turn; a 0 here would be fabricated", v)
	}
	// Session totals come from the shutdown rollup (inputTokens already
	// includes the cache reads/writes), and the deriver-set value wins over
	// the span sum.
	wantMetaTokens(t, d.Meta, 462522, 4246)
}

// TestOpencodeDerive_Tokens pins the opencode mapping (observed live,
// 2026-06-12): assistant message data.tokens has total = input + output +
// reasoning + cache.read + cache.write, i.e. input EXCLUDES cache — the
// emitted input total folds it back in.
func TestOpencodeDerive_Tokens(t *testing.T) {
	rows := agentRows("opencode",
		`{"type":"message","id":"m1","time_created":1781000000000,"data":{"role":"user"}}`,
		`{"type":"part","id":"p1","message_id":"m1","time_created":1781000000000,"data":{"type":"text","text":"hi"}}`,
		`{"type":"message","id":"m2","time_created":1781000001000,"data":{"role":"assistant","model":{"providerID":"prov","modelID":"model-z"},"tokens":{"total":9006,"input":300,"output":149,"reasoning":109,"cache":{"write":0,"read":8448}}}}`,
		`{"type":"part","id":"p2","message_id":"m2","time_created":1781000001000,"data":{"type":"text","text":"hello"}}`,
	)
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, _ := splitSpanKinds(d.Spans)
	wantAttrs(t, llm, map[string]any{
		"gen_ai.usage.input_tokens":                300 + 8448, // cache folds into the total
		"gen_ai.usage.output_tokens":               149 + 109,  // reasoning bills output-side
		"gen_ai.usage.cache_read_input_tokens":     8448,
		"gen_ai.usage.cache_creation_input_tokens": 0,
	})
	wantMetaTokens(t, d.Meta, 8748, 258)
}

// TestHermesDerive_SessionTokens pins the hermes mapping: usage exists only
// at session level (the header row's columns, where input_tokens EXCLUDES the
// cache columns — observed live, 2026-06-12), so meta totals fold cache in
// while the turns themselves stay honestly token-less.
func TestHermesDerive_SessionTokens(t *testing.T) {
	rows := agentRows("hermes",
		`{"type":"session","id":"h-1","model":"nemotron-x","input_tokens":18615,"output_tokens":1096,"cache_read_tokens":156288,"cache_write_tokens":0}`,
		`{"type":"message","role":"user","content":"hi","timestamp":1781000000}`,
		`{"type":"message","role":"assistant","content":"hello","timestamp":1781000001}`,
	)
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	llm, _, _ := splitSpanKinds(d.Spans)
	wantNoUsage(t, llm)
	wantMetaTokens(t, d.Meta, 18615+156288, 1096)
}

// TestHermesDerive_ZeroTokenHeader pins the DEFAULT-0 guard: an all-zero
// header pair means "not recorded" (the columns default to 0), so the meta
// totals stay nil rather than reading as a genuine zero.
func TestHermesDerive_ZeroTokenHeader(t *testing.T) {
	rows := agentRows("hermes",
		`{"type":"session","id":"h-2","model":"nemotron-x","input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0}`,
		`{"type":"message","role":"user","content":"hi","timestamp":1781000000}`,
	)
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.Meta.TokensIn != nil || d.Meta.TokensOut != nil {
		t.Errorf("meta tokens = %v/%v, want nil/nil for an all-zero (unrecorded) header", d.Meta.TokensIn, d.Meta.TokensOut)
	}
}
