package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/store"
)

// TestOpsEval_NoDatabase pins the helpful error when nothing has been captured.
func TestOpsEval_NoDatabase(t *testing.T) {
	isolatedHome(t, true)
	dir := writeGuardTestsFixture(t)
	_, _, err := runRoot(t, nil, "ops", "eval", "run", "guard-tests", "--skill-dir", dir)
	if err == nil || !strings.Contains(err.Error(), "audit database") {
		t.Errorf("want a 'no audit database' error, got %v", err)
	}
}

// TestOpsEval_UnknownSkillDir rejects a --skill-dir with no SKILL.md.
func TestOpsEval_UnknownSkillDir(t *testing.T) {
	isolatedHome(t, true)
	_, _, err := runRoot(t, nil, "ops", "eval", "run", "ghost", "--skill-dir", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "SKILL.md") {
		t.Errorf("want a missing-SKILL.md error, got %v", err)
	}
}

// TestOpsEval_UnknownSuite reports the available suites.
func TestOpsEval_UnknownSuite(t *testing.T) {
	isolatedHome(t, true)
	cfg, _ := config.Load()
	dir := writeGuardTestsFixture(t)
	seedSession(t, cfg, sessionSeed{StartedMs: 1000, Skill: "guard-tests", Outcome: "success", Tools: []string{"Bash"}})

	_, _, err := runRoot(t, nil, "ops", "eval", "run", "guard-tests", "--skill-dir", dir, "--suite", "nope")
	if err == nil || !strings.Contains(err.Error(), "verifies-changes") {
		t.Errorf("want an error listing available suites, got %v", err)
	}
}

// TestOpsEval_NoSessions errors helpfully when the skill has no captured run.
func TestOpsEval_NoSessions(t *testing.T) {
	isolatedHome(t, true)
	cfg, _ := config.Load()
	dir := writeGuardTestsFixture(t)
	// Create the DB (a session for a DIFFERENT skill) so the DB exists but the
	// target skill has no runs.
	seedSession(t, cfg, sessionSeed{StartedMs: 1000, Skill: "other-skill", Outcome: "success", Tools: []string{"Bash"}})

	_, _, err := runRoot(t, nil, "ops", "eval", "run", "guard-tests", "--skill-dir", dir)
	if err == nil || !strings.Contains(err.Error(), "no captured sessions") {
		t.Errorf("want a 'no captured sessions' error, got %v", err)
	}
}

// TestOpsPromote_Gate proves the evidence gate: without a passing eval the
// promote refuses (non-zero), and --force-no-eval overrides it.
func TestOpsPromote_Gate(t *testing.T) {
	isolatedHome(t, true)
	cfg, _ := config.Load()
	dir := writeGuardTestsFixture(t)
	seedSession(t, cfg, sessionSeed{StartedMs: 1000, Skill: "guard-tests", Outcome: "success", Tools: []string{"Bash"}})

	// No eval recorded yet → refuse.
	if _, _, err := runRoot(t, nil, "ops", "promote", "guard-tests", "--skill-dir", dir); err == nil {
		t.Error("expected promote to refuse without a passing eval")
	}

	// Forced → allowed, and the JSON records the override.
	out, _, err := runRoot(t, nil, "ops", "promote", "guard-tests", "--skill-dir", dir, "--force-no-eval", "--output", "json")
	if err != nil {
		t.Fatalf("forced promote: %v", err)
	}
	var d struct {
		Promoted bool `json:"promoted"`
		Forced   bool `json:"forced"`
	}
	if e := json.Unmarshal([]byte(out), &d); e != nil {
		t.Fatalf("decode: %v\n%s", e, out)
	}
	if !d.Promoted || !d.Forced {
		t.Errorf("forced promote = %+v, want promoted+forced", d)
	}
}

// TestPromoteDecision unit-tests the pure gate logic across its three branches.
func TestPromoteDecision(t *testing.T) {
	rs := &resolvedSkill{Name: "guard-tests", Commit: "abc1234"}

	t.Run("passing eval clears it", func(t *testing.T) {
		promoteForce = false
		d := promoteDecision(rs, &store.EvalRunRow{ID: 7, Pass: true})
		if !d.Promoted || d.Forced {
			t.Errorf("want promoted, not forced: %+v", d)
		}
	})
	t.Run("no eval refuses", func(t *testing.T) {
		promoteForce = false
		if d := promoteDecision(rs, nil); d.Promoted {
			t.Errorf("want refusal, got %+v", d)
		}
	})
	t.Run("force overrides", func(t *testing.T) {
		promoteForce = true
		d := promoteDecision(rs, nil)
		if !d.Promoted || !d.Forced {
			t.Errorf("want forced promote: %+v", d)
		}
		promoteForce = false
	})
}

// TestOpsLineage_Empty pins the no-data path: empty JSON, exit 0.
func TestOpsLineage_Empty(t *testing.T) {
	isolatedHome(t, true)
	out, _, err := runRoot(t, nil, "ops", "lineage", "guard-tests", "--output", "json")
	if err != nil {
		t.Fatalf("lineage on empty: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("want empty array, got %q", out)
	}
}
