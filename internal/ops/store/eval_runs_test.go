package store

import (
	"context"
	"testing"
)

// TestEvalRuns_RoundTrip pins the write/read path: a run and its case rows land
// atomically, read back newest-first, and filter by skill commit.
func TestEvalRuns_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Two runs of the same skill at different commits: the older fails, the
	// newer (post-fix) passes — the article's fail→pass.
	if _, err := st.PutEvalRun(ctx, &EvalRunRow{
		SkillName: "triage-issue", SkillCommit: "aaaaaaa", Suite: "triage-correctness",
		Passed: 0, Failed: 1, Pass: false,
		Cases: []EvalCaseRow{{Suite: "triage-correctness", Case: "needs-info", Pass: false, Detail: "text: missing needs-info"}},
	}); err != nil {
		t.Fatalf("put failing run: %v", err)
	}
	id2, err := st.PutEvalRun(ctx, &EvalRunRow{
		SkillName: "triage-issue", SkillCommit: "bbbbbbb", Suite: "triage-correctness",
		Passed: 1, Failed: 0, Pass: true,
		Cases: []EvalCaseRow{{Suite: "triage-correctness", Case: "needs-info", Pass: true}},
	})
	if err != nil {
		t.Fatalf("put passing run: %v", err)
	}
	if id2 == 0 {
		t.Fatal("expected a non-zero run id")
	}

	all, err := st.ListEvalRuns(ctx, &EvalRunFilter{SkillName: "triage-issue"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 runs, got %d", len(all))
	}
	// Newest-first: the passing run leads.
	if !all[0].Pass || all[0].SkillCommit != "bbbbbbb" {
		t.Errorf("newest run wrong: %+v", all[0])
	}
	if len(all[0].Cases) != 1 || !all[0].Cases[0].Pass {
		t.Errorf("case rows did not round-trip: %+v", all[0].Cases)
	}
	if len(all[1].Cases) != 1 || all[1].Cases[0].Detail != "text: missing needs-info" {
		t.Errorf("failing case detail did not round-trip: %+v", all[1].Cases)
	}

	// Commit filter isolates the passing run.
	byCommit, _ := st.ListEvalRuns(ctx, &EvalRunFilter{SkillName: "triage-issue", SkillCommit: "bbbbbbb"})
	if len(byCommit) != 1 || !byCommit[0].Pass {
		t.Errorf("commit filter: want 1 passing, got %+v", byCommit)
	}
}

// TestEvalRuns_RequiresRow pins the nil guard.
func TestEvalRuns_RequiresRow(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.PutEvalRun(context.Background(), nil); err == nil {
		t.Error("expected an error for a nil eval run")
	}
}
