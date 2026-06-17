package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// TestParse_RejectsNoOpGraders pins the hardening rule: a grader with no real
// assertion (a gate that can never fail) is a config error, not a silent pass.
func TestParse_RejectsNoOpGraders(t *testing.T) {
	cases := []struct {
		name    string
		grader  string
		wantErr bool
	}{
		{"outcome ok", "type: outcome\n            expect: success", false},
		{"outcome no expect", "type: outcome", true},
		{"text ok", "type: text\n            contains: [\"x\"]", false},
		{"text empty", "type: text", true},
		{"text on final_message", "type: text\n            on: final_message\n            contains: [\"x\"]", false},
		{"text on unknown", "type: text\n            on: message\n            contains: [\"x\"]", true},
		{"sequence empty", "type: tool_sequence", true},
		{"constraint empty", "type: tool_constraint", true},
		{"constraint maxTools ok", "type: tool_constraint\n            maxTools: 5", false},
		{"skill_invocation empty", "type: skill_invocation", true},
		{"behavior empty", "type: behavior", true},
		{"behavior maxTurns ok", "type: behavior\n            maxTurns: 3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			y := "version: 1\nsuites:\n  - name: s\n    cases:\n      - name: c\n        graders:\n          - " + tc.grader + "\n"
			_, err := Parse([]byte(y))
			if (err != nil) != tc.wantErr {
				t.Errorf("Parse err=%v, wantErr=%v\nyaml:\n%s", err, tc.wantErr, y)
			}
		})
	}
}

// TestLoad_AbsentManifestIsNil pins the contract: a skill with no evals.yaml is
// not an error (it just has no evals), while malformed YAML is.
func TestLoad_AbsentManifestIsNil(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil || m != nil {
		t.Errorf("absent manifest: got (%v, %v), want (nil, nil)", m, err)
	}

	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte("version: 1\nsuites: [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("expected an error for malformed evals.yaml")
	}
}

// TestBuildEvidence distils a captured session into grader input: the final
// assistant message and the tool sequence (ordered by start time, even when the
// store hands spans back out of order).
func TestBuildEvidence(t *testing.T) {
	sid := uuid.New()
	meta := &store.SessionMetaRow{
		SessionID: sid, Outcome: "success", Skills: []string{"triage-issue"},
		Tools: 2, Turns: 1, StartedMs: 1000, EndedMs: 2500,
	}
	// Spans deliberately out of order; the second tool starts later.
	spans := []*store.SpanRow{
		{Kind: "TOOL", StartMs: 1200, Attributes: `{"gen_ai.tool.name":"Edit"}`},
		{Kind: "LLM", StartMs: 1000, Attributes: `{"gen_ai.output.messages":"[{\"role\":\"assistant\",\"content\":\"Labeled needs-info\"}]"}`},
		{Kind: "TOOL", StartMs: 1100, Attributes: `{"gen_ai.tool.name":"Read"}`},
	}
	e := BuildEvidence(meta, spans)
	if e.Outcome != "success" || e.FinalMessage != "Labeled needs-info" {
		t.Errorf("evidence basics wrong: %+v", e)
	}
	if len(e.ToolSequence) != 2 || e.ToolSequence[0] != "Read" || e.ToolSequence[1] != "Edit" {
		t.Errorf("tool sequence not ordered by start time: %v", e.ToolSequence)
	}
	if e.DurationMs != 1500 {
		t.Errorf("duration = %d, want 1500", e.DurationMs)
	}
}

// TestBuildEvidence_ToleratesGarbage pins robustness: malformed or empty span
// attribute blobs must degrade gracefully (empty fields), never panic.
func TestBuildEvidence_ToleratesGarbage(t *testing.T) {
	meta := &store.SessionMetaRow{SessionID: uuid.New()}
	spans := []*store.SpanRow{
		{Kind: "TOOL", StartMs: 1, Attributes: "not json{"},
		{Kind: "LLM", StartMs: 2, Attributes: ""},
		{Kind: "TOOL", StartMs: 3, Attributes: `{"gen_ai.tool.name":"Read"}`},
	}
	e := BuildEvidence(meta, spans)
	if e.FinalMessage != "" {
		t.Errorf("want empty final message from garbage, got %q", e.FinalMessage)
	}
	if len(e.ToolSequence) != 1 || e.ToolSequence[0] != "Read" {
		t.Errorf("want only the well-formed tool, got %v", e.ToolSequence)
	}
}

func TestParse_ValidatesVersionAndStructure(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"good", `
version: 1
suites:
  - name: triage-correctness
    cases:
      - name: needs-info
        graders:
          - type: text
            contains: ["needs-info"]
`, false},
		{"bad version", "version: 2\nsuites: []\n", true},
		{"no suites", "version: 1\nsuites: []\n", true},
		{"suite no cases", "version: 1\nsuites:\n  - name: s\n    cases: []\n", true},
		{"unknown grader", `
version: 1
suites:
  - name: s
    cases:
      - name: c
        graders:
          - type: prompt
`, true},
		{"case no graders", `
version: 1
suites:
  - name: s
    cases:
      - name: c
        graders: []
`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if (err != nil) != tc.wantErr {
				t.Errorf("Parse err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func ev() *Evidence {
	return &Evidence{
		SessionID:    "s1",
		Skill:        "triage-issue",
		Outcome:      "success",
		FinalMessage: "Labeled the issue needs-info because the requirement is ambiguous.",
		ToolSequence: []string{"Read", "Bash", "Edit"},
		Skills:       []string{"triage-issue"},
		Tools:        3,
		Turns:        2,
		DurationMs:   1500,
	}
}

func TestGraders(t *testing.T) {
	cases := []struct {
		name string
		spec GraderSpec
		want bool
	}{
		{"outcome pass", GraderSpec{Type: "outcome", Expect: "success"}, true},
		{"outcome fail", GraderSpec{Type: "outcome", Expect: "failure"}, false},
		{"text contains pass", GraderSpec{Type: "text", Contains: []string{"needs-info"}}, true},
		{"text contains fail", GraderSpec{Type: "text", Contains: []string{"ready-to-implement"}}, false},
		{"text reject pass", GraderSpec{Type: "text", Reject: []string{"ready-to-implement"}}, true},
		{"text reject fail", GraderSpec{Type: "text", Reject: []string{"needs-info"}}, false},
		{"sequence pass", GraderSpec{Type: "tool_sequence", Sequence: []string{"Read", "Edit"}}, true},
		{"sequence fail (order)", GraderSpec{Type: "tool_sequence", Sequence: []string{"Edit", "Read"}}, false},
		{"constraint expect pass", GraderSpec{Type: "tool_constraint", ExpectTools: []string{"Bash"}}, true},
		{"constraint reject fail", GraderSpec{Type: "tool_constraint", RejectTools: []string{"Bash"}}, false},
		{"constraint max fail", GraderSpec{Type: "tool_constraint", MaxTools: 2}, false},
		{"skill expect pass", GraderSpec{Type: "skill_invocation", ExpectSkills: []string{"triage-issue"}}, true},
		{"skill reject fail", GraderSpec{Type: "skill_invocation", RejectSkills: []string{"triage-issue"}}, false},
		{"behavior pass", GraderSpec{Type: "behavior", MaxTools: 5, MaxTurns: 3}, true},
		{"behavior fail turns", GraderSpec{Type: "behavior", MaxTurns: 1}, false},
		{"behavior fail duration", GraderSpec{Type: "behavior", MaxDurationMs: 1000}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runGrader(tc.spec, ev())
			if got.Pass != tc.want {
				t.Errorf("grader %s: pass=%v, want %v (detail=%q)", tc.spec.Type, got.Pass, tc.want, got.Detail)
			}
		})
	}
}

// TestGradeSuite_AllMustPass pins the AND semantics: one failing grader fails
// the case, and one failing case fails the suite.
func TestGradeSuite_AllMustPass(t *testing.T) {
	s := &Suite{Name: "triage-correctness", Cases: []Case{
		{Name: "needs-info", Graders: []GraderSpec{
			{Type: "outcome", Expect: "success"},
			{Type: "text", Contains: []string{"needs-info"}, Reject: []string{"ready-to-implement"}},
		}},
	}}
	if r := GradeSuite(s, ev()); !r.Pass || r.Passed != 1 || r.Failed != 0 {
		t.Errorf("expected suite pass, got %+v", r)
	}

	// Flip the evidence to the pre-improvement (misclassified) run.
	bad := ev()
	bad.FinalMessage = "Labeled the issue ready-to-implement."
	if r := GradeSuite(s, bad); r.Pass || r.Failed != 1 {
		t.Errorf("expected suite fail on misclassification, got %+v", r)
	}
}
