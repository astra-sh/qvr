package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
)

// TestAcceptance_BehavioralGate exercises the loop on a DIFFERENT dimension than
// the triage label-match: a skill that must verify its edits by running tests.
// The gate here is behavioral — tool sequence, tool constraints, skill
// invocation, efficiency — so it proves the eval substrate generalizes past
// simple text matching. First run edits without testing (fails); the corrected
// run edits THEN tests (passes).
func TestAcceptance_BehavioralGate(t *testing.T) {
	isolatedHome(t, true)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	skillDir := writeGuardTestsFixture(t)

	// 1. A run that edited code but never ran the tests (Read, Edit — no Bash).
	badID := seedSession(t, cfg, sessionSeed{
		StartedMs: 1000, Skill: "guard-tests", FinalMsg: "Applied the change.",
		Outcome: "success", Tools: []string{"Read", "Edit"},
	})

	// 2. Reviewer flags the unverified change.
	if _, stderr, err := runRoot(t, nil, "audit", "annotate", badID,
		"--skill", "guard-tests", "--outcome", "bad", "--note", "shipped without running tests"); err != nil {
		t.Fatalf("annotate: err=%v stderr=%q", err, stderr)
	}

	// 3. Gate FAILS: tool_sequence Edit→Bash unsatisfied, Bash missing.
	if _, _, err := runRoot(t, nil, "ops", "eval", "run", "guard-tests",
		"--skill-dir", skillDir, "--output", "json"); err == nil {
		t.Fatal("expected the baseline eval to FAIL for an unverified change")
	}

	// 4. Corrected run: Read, Edit, THEN Bash (runs the tests).
	seedSession(t, cfg, sessionSeed{
		StartedMs: 2000, Skill: "guard-tests", FinalMsg: "Applied the change and ran the tests.",
		Outcome: "success", Tools: []string{"Read", "Edit", "Bash"},
	})

	// 5. Gate PASSES.
	out, _, err := runRoot(t, nil, "ops", "eval", "run", "guard-tests",
		"--skill-dir", skillDir, "--output", "json")
	if err != nil {
		t.Fatalf("expected the post-fix eval to PASS, got err=%v", err)
	}
	var res struct {
		Pass   bool `json:"pass"`
		Failed int  `json:"failed"`
	}
	if e := json.Unmarshal([]byte(out), &res); e != nil {
		t.Fatalf("decode eval json: %v\n%s", e, out)
	}
	if !res.Pass || res.Failed != 0 {
		t.Fatalf("post-fix eval = %+v, want pass with 0 failures", res)
	}
}

// writeGuardTestsFixture creates a fixture skill whose suite asserts the skill
// fired, edits were followed by a test run, and no network was used — all
// behavioral graders over the captured trace.
func writeGuardTestsFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "guard-tests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := `---
name: guard-tests
description: Edits code and always verifies the change by running the test suite.
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# Guard tests

After editing code, always run the test suite to verify the change before
reporting done. Never ship an edit you have not verified.
`
	evals := `version: 1
suites:
  - name: verifies-changes
    cases:
      - name: edits-then-runs-tests
        graders:
          - type: skill_invocation
            expectSkills: ["guard-tests"]
          - type: tool_sequence
            sequence: ["Edit", "Bash"]
          - type: tool_constraint
            expectTools: ["Bash"]
            rejectTools: ["WebFetch"]
          - type: behavior
            maxTools: 10
`
	mustWrite(t, filepath.Join(dir, "SKILL.md"), skill)
	mustWrite(t, filepath.Join(dir, "evals.yaml"), evals)
	return dir
}
