package derive_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// These tests enforce 1:1 parity between a REAL captured agent transcript and
// its derived span tree: every tool/skill call in the raw bytes becomes exactly
// one child span, every turn gets a root + model span, the nesting is intact,
// and the clean titles never drop queryable detail. The expected counts are
// computed from the fixture's own raw bytes (not hardcoded), so the guard moves
// with the data — any future deriver change that silently drops or duplicates a
// call fails here. Fixtures are byte-faithful real sessions (home path
// anonymized), per the project's real-traces-only testing rule.

// realRows loads a fixture as one transcript RawTrace per JSONL line.
func realRows(t *testing.T, agent, name string) []*ops.RawTrace {
	t.Helper()
	path := filepath.Join("testdata", "real", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	sid := uuid.NewSHA1(uuid.NameSpaceURL, []byte(name))
	var rows []*ops.RawTrace
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, &ops.RawTrace{
			AgentName:  agent,
			SessionID:  sid,
			Source:     ops.RawSourceTranscript,
			Seq:        i,
			CapturedAt: time.Now(),
			Raw:        []byte(line),
		})
	}
	if len(rows) == 0 {
		t.Fatalf("fixture %s had no rows", name)
	}
	return rows
}

// rawClaudeToolUses counts tool_use blocks across a claude transcript's
// assistant lines — the number of tool/skill calls the run actually made.
func rawClaudeToolUses(rows []*ops.RawTrace) int {
	n := 0
	for _, r := range rows {
		var ln struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(r.Raw, &ln) != nil || ln.Type != "assistant" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(ln.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" {
				n++
			}
		}
	}
	return n
}

// rawCodexToolCalls counts a codex rollout's function_call + custom_tool_call
// items — the run's actual tool calls.
func rawCodexToolCalls(rows []*ops.RawTrace) int {
	n := 0
	for _, r := range rows {
		var ln struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		if json.Unmarshal(r.Raw, &ln) != nil || ln.Type != "response_item" {
			continue
		}
		if ln.Payload.Type == "function_call" || ln.Payload.Type == "custom_tool_call" {
			n++
		}
	}
	return n
}

// assertCleanTree checks the structural invariants common to every clean span
// tree: a root per model span, intact parent links, and clean child titles.
func assertCleanTree(t *testing.T, spans []derive.Span, wantRootName, wantModelName string) {
	t.Helper()
	roots, llms := indexTreeNodes(t, spans, wantRootName, wantModelName)
	for _, s := range spans {
		assertSpanParenting(t, s, roots, llms)
	}
}

// indexTreeNodes collects the root and model span id sets, checking their names
// and that there is one root per model span.
func indexTreeNodes(t *testing.T, spans []derive.Span, wantRootName, wantModelName string) (roots, llms map[string]bool) {
	t.Helper()
	roots, llms = map[string]bool{}, map[string]bool{}
	for _, s := range spans {
		switch s.Kind {
		case derive.KindChain:
			roots[s.SpanID] = true
			if s.Name != wantRootName {
				t.Errorf("root span name = %q, want %q", s.Name, wantRootName)
			}
		case derive.KindLLM:
			llms[s.SpanID] = true
			if s.Name != wantModelName {
				t.Errorf("model span name = %q, want %q", s.Name, wantModelName)
			}
		}
	}
	if len(roots) == 0 || len(roots) != len(llms) {
		t.Errorf("want one root per model span, got %d roots / %d model spans", len(roots), len(llms))
	}
	return roots, llms
}

// assertSpanParenting checks one span's parent link and (for children) its clean
// title plus retained gen_ai.tool.name.
func assertSpanParenting(t *testing.T, s derive.Span, roots, llms map[string]bool) {
	t.Helper()
	switch s.Kind {
	case derive.KindLLM:
		if !roots[s.ParentSpanID] {
			t.Errorf("model span %q parent %q is not a root span", s.Name, s.ParentSpanID)
		}
	case derive.KindTool, derive.KindSkill:
		if !llms[s.ParentSpanID] {
			t.Errorf("%s span %q parent %q is not a model span", s.Kind, s.Name, s.ParentSpanID)
		}
		if s.Name == "" {
			t.Errorf("%s span has empty title", s.Kind)
		}
		if strings.HasPrefix(s.Name, "execute_tool ") || strings.HasPrefix(s.Name, "mcp__") {
			t.Errorf("%s span title not cleaned: %q", s.Kind, s.Name)
		}
		if _, ok := s.Attributes["gen_ai.tool.name"]; !ok {
			t.Errorf("%s span %q dropped gen_ai.tool.name", s.Kind, s.Name)
		}
	}
}

// countToolSkillSpans returns the number of TOOL spans plus SKILL spans that
// correspond to a raw tool call — i.e. excluding the additive injection/implicit
// skill loads, which have no tool_use/function_call to pair with. This is the
// count to compare against the raw tool-call count for 1:1 parity.
func countToolSkillSpans(spans []derive.Span) int {
	n := 0
	for _, s := range spans {
		switch s.Kind {
		case derive.KindTool:
			n++
		case derive.KindSkill:
			a, _ := s.Attributes[derive.SkillActivationKey].(string)
			if a != derive.SkillActivationInjection && a != derive.SkillActivationImplicit {
				n++
			}
		}
	}
	return n
}

func TestRealParity_Claude(t *testing.T) {
	rows := realRows(t, "claude-code", "claude_skill.jsonl")
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if tools, want := countToolSkillSpans(d.Spans), rawClaudeToolUses(rows); tools != want {
		t.Errorf("derived %d tool/skill spans, raw has %d tool_use blocks (parity lost)", tools, want)
	}
	assertCleanTree(t, d.Spans, "Claude Code Turn", "Claude")
	// The fixture loaded a skill — it must surface as a SKILL span and in meta.
	if len(d.Meta.Skills) == 0 {
		t.Errorf("real skill session derived no skills")
	}
}

func TestRealParity_Codex(t *testing.T) {
	rows := realRows(t, "codex", "codex_skill.jsonl")
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if tools, want := countToolSkillSpans(d.Spans), rawCodexToolCalls(rows); tools != want {
		t.Errorf("derived %d tool/skill spans, raw has %d codex tool calls (parity lost)", tools, want)
	}
	assertCleanTree(t, d.Spans, "Codex Turn", "Codex")
	if len(d.Meta.Skills) == 0 {
		t.Errorf("real skill session derived no skills")
	}
	// Codex persists the final assistant text twice — once as an event_msg
	// agent_message and once as a response_item message(assistant). The turn
	// output must fold it in exactly ONCE; the response_item is the source of
	// truth and the event is only mined for narration.
	assertCodexOutputNotDoubled(t, rows, d.Spans)
}

// assertCodexOutputNotDoubled checks the derived turn output equals the raw
// assistant message(s) once, not concatenated with the duplicate agent_message
// event. It compares against the raw response_item assistant text so the guard
// moves with the fixture instead of hardcoding the body.
func assertCodexOutputNotDoubled(t *testing.T, rows []*ops.RawTrace, spans []derive.Span) {
	t.Helper()
	want := rawCodexAssistantText(rows)
	if want == "" {
		t.Fatalf("fixture has no response_item assistant text to compare against")
	}
	for _, s := range spans {
		if s.Kind != "LLM" {
			continue
		}
		got := outputMessageContent(t, s)
		if got != want {
			t.Errorf("turn output = %q, want %q (duplicate agent_message folded in?)", got, want)
		}
	}
}

// rawCodexAssistantText concatenates the output_text of every response_item
// message(role=assistant) in the raw rows, newline-joined — the canonical
// assistant output the turn should reproduce exactly once.
func rawCodexAssistantText(rows []*ops.RawTrace) string {
	var parts []string
	for _, r := range rows {
		var ln struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(r.Raw, &ln) != nil {
			continue
		}
		if ln.Type != "response_item" || ln.Payload.Type != "message" || ln.Payload.Role != "assistant" {
			continue
		}
		for _, b := range ln.Payload.Content {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// outputMessageContent extracts the assistant content from a span's
// gen_ai.output.messages attribute (the JSON [{role,content}] array).
func outputMessageContent(t *testing.T, s derive.Span) string {
	t.Helper()
	raw, _ := s.Attributes["gen_ai.output.messages"].(string)
	if raw == "" {
		return ""
	}
	var msgs []struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatalf("unmarshal output.messages: %v", err)
	}
	var parts []string
	for _, m := range msgs {
		parts = append(parts, m.Content)
	}
	return strings.Join(parts, "\n")
}

func TestRealParity_ClaudeSubagentNesting(t *testing.T) {
	// Claude Code records a subagent in a sibling file that ingests under the
	// SAME session as its parent; combine the real parent + child fixtures into
	// one session, exactly as the store holds them.
	parent := realRows(t, "claude-code", "claude_subagent_parent.jsonl")
	child := realRows(t, "claude-code", "claude_subagent_child.jsonl")
	sid := parent[0].SessionID
	var rows []*ops.RawTrace
	rows = append(rows, parent...)
	for i, r := range child {
		c := *r
		c.SessionID = sid // subagent lines carry the parent's sessionId in the wild
		c.Seq = len(parent) + i
		rows = append(rows, &c)
	}

	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}

	// 1:1 parity: every tool_use across BOTH files becomes exactly one span.
	if tools, want := countToolSkillSpans(d.Spans), rawClaudeToolUses(rows); tools != want {
		t.Errorf("derived %d tool/skill spans, raw has %d tool_use blocks (parity lost)", tools, want)
	}

	subRoots, nestedUnderAgent := countSubagentNesting(d.Spans)
	if subRoots == 0 {
		t.Fatalf("subagent session derived no subagent-depth root spans")
	}
	if nestedUnderAgent == 0 {
		t.Errorf("subagent roots did not nest under an Agent tool span (%d sub roots)", subRoots)
	}
}

// countSubagentNesting returns how many subagent-depth root spans exist and how
// many of those hang under an Agent tool span.
func countSubagentNesting(spans []derive.Span) (subRoots, nestedUnderAgent int) {
	byID := map[string]derive.Span{}
	for _, s := range spans {
		byID[s.SpanID] = s
	}
	for _, s := range spans {
		if s.Kind != derive.KindChain {
			continue
		}
		depth, _ := s.Attributes["qvr.run_depth"].(int)
		if atype, _ := s.Attributes["qvr.agent_type"].(string); depth < 1 || atype != "subagent" {
			continue
		}
		subRoots++
		parent, ok := byID[s.ParentSpanID]
		if !ok {
			continue
		}
		if name, _ := parent.Attributes["gen_ai.tool.name"].(string); name == "Agent" || name == "Task" {
			nestedUnderAgent++
		}
	}
	return subRoots, nestedUnderAgent
}

func TestRealParity_ClaudeInjectedSkill(t *testing.T) {
	// A real session that loaded a skill via the harness "Base directory for this
	// skill:" injection with NO preceding Skill tool call — the load the deriver
	// used to drop. It must now surface as a SKILL span with activation=injection.
	rows := realRows(t, "claude-code", "claude_injected_skill.jsonl")
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	assertCleanTree(t, d.Spans, "Claude Code Turn", "Claude")
	var injected int
	for _, s := range d.Spans {
		if s.Kind != derive.KindSkill {
			continue
		}
		if a, _ := s.Attributes[derive.SkillActivationKey].(string); a == derive.SkillActivationInjection {
			injected++
			if _, ok := s.Attributes["skill.name"]; !ok {
				t.Errorf("injected skill span missing skill.name")
			}
		}
	}
	if injected == 0 {
		t.Errorf("injected-skill session derived no injection-activation SKILL span")
	}
	if len(d.Meta.Skills) == 0 {
		t.Errorf("injected-skill session derived no skills in meta")
	}
}

func TestRealParity_CodexNarratedSkill(t *testing.T) {
	// A real codex session that BOTH narrated "Using the `skillops-sql` skill" AND
	// read its SKILL.md by the resolved two-segment worktree path. The strong
	// signal wins and must not be double-counted: the read attributes one
	// path-activation SKILL span, and the catalog-gated narration must dedupe
	// against it rather than emit a second (implicit) span for the same skill.
	// (Before the multi-segment worktree fix, the read silently failed to match
	// and narration was the only surviving signal — so this fixture was attributed
	// implicitly, masking both the path bug and the dedup it now exercises.)
	rows := realRows(t, "codex", "codex_narrated_skill.jsonl")
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	assertCleanTree(t, d.Spans, "Codex Turn", "Codex")
	var spans []*derive.Span
	for i := range d.Spans {
		s := &d.Spans[i]
		if s.Kind != derive.KindSkill {
			continue
		}
		if name, _ := s.Attributes["skill.name"].(string); name == "skillops-sql" {
			spans = append(spans, s)
		}
	}
	if len(spans) != 1 {
		t.Fatalf("skillops-sql SKILL spans = %d, want exactly 1 (narration must dedupe against the path load)", len(spans))
	}
	if a, _ := spans[0].Attributes[derive.SkillActivationKey].(string); a != derive.SkillActivationPath {
		t.Errorf("activation = %q, want %q (the SKILL.md read is the strong signal, not narration)", a, derive.SkillActivationPath)
	}
	if len(d.Meta.Skills) == 0 {
		t.Errorf("narrated-skill session derived no skills in meta")
	}
}

func TestRealParity_CodexWorktreeSkill(t *testing.T) {
	// A real codex session whose ONLY skill signal is a read of the resolved store
	// path — /Users/u/.quiver/worktrees/<org>/<repo>/<skill>/<sha7>/SKILL.md — with
	// no narration and no agent-dir read to fall back on. The store nests
	// <org>/<repo> as two path segments, which a single-segment worktree matcher
	// could not parse: it found no skill span, and the skill-only retention gate
	// then dropped the whole session (claude reads the relative agent-dir symlink
	// and was never affected, hiding the gap behind a clean claude column). The
	// worktree path must attribute as a path-activation SKILL span so the session
	// is kept, and its sha+registry must resolve identity from the recorded bytes.
	rows := realRows(t, "codex", "codex_worktree_skill.jsonl")
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	const wantPath = "/Users/u/.quiver/worktrees/qvr-regex/regex-rules/regex-rules/f17a6ae/SKILL.md"
	var span *derive.Span
	for i := range d.Spans {
		s := &d.Spans[i]
		if s.Kind != derive.KindSkill {
			continue
		}
		if name, _ := s.Attributes["skill.name"].(string); name == "regex-rules" {
			span = s
		}
	}
	if span == nil {
		t.Fatalf("worktree-path session derived no regex-rules SKILL span (session would be dropped by the skill gate)")
	}
	if a, _ := span.Attributes[derive.SkillActivationKey].(string); a != derive.SkillActivationPath {
		t.Errorf("activation = %q, want %q (the worktree path is the attribution signal)", a, derive.SkillActivationPath)
	}
	if got, _ := span.Attributes["skill.load_path"].(string); got != wantPath {
		t.Errorf("load_path = %q, want the two-segment worktree path %q", got, wantPath)
	}
	if len(d.Meta.Skills) == 0 {
		t.Errorf("worktree-path session derived no skills in meta")
	}
	// End-to-end: the two-segment store path must also resolve identity — the
	// sha and the full <org>/<repo> registry both live in the recorded bytes, so
	// enrichment pins commit + registry with no disk access. A single-segment
	// matcher on either the detection or the enrichment regex breaks this.
	derive.EnrichSkillIdentity(d.Spans, rows, nil)
	if got, _ := span.Attributes["skill.commit"].(string); got != "f17a6ae" {
		t.Errorf("skill.commit = %q, want f17a6ae recovered from the worktree path", got)
	}
	if got, _ := span.Attributes["skill.registry"].(string); got != "qvr-regex/regex-rules" {
		t.Errorf("skill.registry = %q, want the full two-segment registry", got)
	}
}

func TestRealParity_ClaudeReasoning(t *testing.T) {
	rows := realRows(t, "claude-code", "claude_reasoning.jsonl")
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	assertCleanTree(t, d.Spans, "Claude Code Turn", "Claude")
	// This fixture has unencrypted thinking text — it must ride on a model span.
	var sawReasoning bool
	for _, s := range d.Spans {
		if s.Kind != derive.KindLLM {
			continue
		}
		if r, _ := s.Attributes["qvr.reasoning"].(string); strings.TrimSpace(r) != "" {
			sawReasoning = true
		}
	}
	if !sawReasoning {
		t.Errorf("reasoning fixture derived no qvr.reasoning on any model span")
	}
}
