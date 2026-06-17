package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// TestAcceptance_SelfImprovementLoop recreates the article's triage loop end to
// end through the real CLI: a skill mis-triages an issue, a human flags it, the
// eval gate FAILS, the skill is "improved" (a corrected run), and the gate now
// PASSES — with the whole arc visible in `qvr ops lineage`. The fail→pass is the
// branch's acceptance criterion.
func TestAcceptance_SelfImprovementLoop(t *testing.T) {
	isolatedHome(t, true)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	skillDir := writeTriageFixture(t)

	// 1. INNER LOOP: a real run that mis-triaged the issue (the article's
	//    "ready-to-implement" mistake). Captured + skill-attributed.
	badID := seedTriageSession(t, cfg, 1000, "Labeled this issue ready-to-implement.")

	// 2. HUMAN FEEDBACK: the reviewer flips the verdict and says why.
	if _, stderr, err := runRoot(t, nil, "audit", "annotate", badID,
		"--skill", "triage-issue", "--outcome", "bad", "--note", "ambiguous — needs a setting, should be needs-info"); err != nil {
		t.Fatalf("annotate: err=%v stderr=%q", err, stderr)
	}

	// 3. BASELINE GATE: the eval must FAIL on the misclassification.
	if _, _, err := runRoot(t, nil, "ops", "eval", "run", "triage-issue",
		"--skill-dir", skillDir, "--suite", "triage-correctness", "--output", "json"); err == nil {
		t.Fatal("expected the baseline eval to FAIL on the misclassified run")
	}

	// 4. IMPROVE: the loop edits the skill and re-runs it; the corrected run is
	//    captured (newer, so it is the most-recent session the eval grades).
	seedTriageSession(t, cfg, 2000, "Labeled this issue needs-info pending a decision.")

	// 5. POST-FIX GATE: the same eval must now PASS.
	out, _, err := runRoot(t, nil, "ops", "eval", "run", "triage-issue",
		"--skill-dir", skillDir, "--suite", "triage-correctness", "--output", "json")
	if err != nil {
		t.Fatalf("expected the post-fix eval to PASS, got err=%v", err)
	}
	var res struct {
		Pass   bool `json:"pass"`
		Passed int  `json:"passed"`
		Failed int  `json:"failed"`
	}
	if e := json.Unmarshal([]byte(out), &res); e != nil {
		t.Fatalf("decode eval json: %v\n%s", e, out)
	}
	if !res.Pass || res.Failed != 0 {
		t.Fatalf("post-fix eval = %+v, want pass with 0 failures", res)
	}

	// 6. LINEAGE: the timeline shows the fail, then the pass, plus the verdict.
	lo, _, err := runRoot(t, nil, "ops", "lineage", "triage-issue", "--output", "json")
	if err != nil {
		t.Fatalf("lineage: %v", err)
	}
	assertFailPassArc(t, lo)
}

// assertFailPassArc checks the lineage timeline carries the article's full arc:
// at least one failed eval, one passed eval, and one human annotation.
func assertFailPassArc(t *testing.T, lineageJSON string) {
	t.Helper()
	var timeline []struct {
		Kind string `json:"kind"`
		Pass *bool  `json:"pass"`
	}
	if e := json.Unmarshal([]byte(lineageJSON), &timeline); e != nil {
		t.Fatalf("decode lineage json: %v\n%s", e, lineageJSON)
	}
	evalPass, evalFail, annotations := 0, 0, 0
	for _, e := range timeline {
		switch {
		case e.Kind == "annotation":
			annotations++
		case e.Pass != nil && *e.Pass:
			evalPass++
		default:
			evalFail++
		}
	}
	if evalPass < 1 || evalFail < 1 || annotations < 1 {
		t.Errorf("lineage missing the fail→pass arc: %d pass, %d fail, %d annotations", evalPass, evalFail, annotations)
	}
}

// writeTriageFixture creates a fixture triage-issue skill with an evals.yaml
// whose correctness suite distinguishes the right label from the wrong one.
func writeTriageFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "triage-issue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := `---
name: triage-issue
description: Sorts incoming issues into ready-to-implement, duplicate, or needs-info.
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# Triage an issue

Classify each incoming issue into exactly one bucket: ready-to-implement,
duplicate, or needs-info. When a feature request leaves an ambiguity (e.g.
whether to add a setting), prefer needs-info over ready-to-implement.
`
	evals := `version: 1
suites:
  - name: triage-correctness
    cases:
      - name: ambiguous-feature-needs-info
        graders:
          - type: outcome
            expect: success
          - type: text
            on: final_message
            contains: ["needs-info"]
            reject: ["ready-to-implement"]
`
	mustWrite(t, filepath.Join(dir, "SKILL.md"), skill)
	mustWrite(t, filepath.Join(dir, "evals.yaml"), evals)
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedTriageSession seeds a successful triage-issue run ending with finalMsg,
// using a single Bash tool. Thin wrapper over seedSession.
func seedTriageSession(t *testing.T, cfg *config.Config, startedMs int64, finalMsg string) string {
	t.Helper()
	return seedSession(t, cfg, sessionSeed{
		StartedMs: startedMs, Skill: "triage-issue", FinalMsg: finalMsg,
		Outcome: "success", Tools: []string{"Bash"},
	})
}

// sessionSeed describes a synthetic captured session to inject straight into the
// audit store (bypassing agent-store discovery), so a test controls exactly the
// evidence the graders read: the skill that fired, the ordered tool calls, the
// session outcome, and the final assistant message.
type sessionSeed struct {
	StartedMs int64
	Skill     string
	FinalMsg  string
	Outcome   string
	Tools     []string // ordered tool names (TOOL spans), after the SKILL span
}

// seedSession writes the synthetic session and returns its id.
func seedSession(t *testing.T, cfg *config.Config, seed sessionSeed) string {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, store.OpenOptions{Path: ops.DBPath(cfg)})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	sid := uuid.New()
	outMsgs, _ := json.Marshal([]map[string]string{{"role": "assistant", "content": seed.FinalMsg}})
	tid := "trace-" + sid.String()[:8]
	end := seed.StartedMs + 1000

	spans := []*store.SpanRow{
		{SpanID: tid + "-llm", TraceID: tid, SessionID: sid, AgentName: "claude-code",
			Kind: "LLM", Name: "chat", StartMs: seed.StartedMs, EndMs: end,
			Attributes: fmt.Sprintf(`{"gen_ai.output.messages":%q}`, string(outMsgs))},
		{SpanID: tid + "-skill", TraceID: tid, SessionID: sid, AgentName: "claude-code",
			Kind: "SKILL", Name: "execute_tool Skill", StartMs: seed.StartedMs + 50, EndMs: seed.StartedMs + 100,
			Attributes: fmt.Sprintf(`{"gen_ai.tool.name":"Skill","skill.name":%q}`, seed.Skill)},
	}
	for i, tool := range seed.Tools {
		at := seed.StartedMs + int64(100*(i+2))
		spans = append(spans, &store.SpanRow{
			SpanID: fmt.Sprintf("%s-tool%d", tid, i), TraceID: tid, SessionID: sid, AgentName: "claude-code",
			Kind: "TOOL", Name: "execute_tool " + tool, StartMs: at, EndMs: at + 50,
			Attributes: fmt.Sprintf(`{"gen_ai.tool.name":%q,"qvr.outcome":"success"}`, tool),
		})
	}
	meta := &store.SessionMetaRow{
		SessionID: sid, AgentName: "claude-code", Model: "claude-opus-4-8",
		Title: seed.Skill + " run", StartedMs: seed.StartedMs, EndedMs: end,
		Turns: 1, Tools: int64(len(seed.Tools)), Skills: []string{seed.Skill}, Outcome: seed.Outcome,
		DeriverVersion: 8,
	}
	if err := s.ReplaceSessionDerivation(ctx, meta, spans); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sid.String()
}
