package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
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

	all, err := st.ListEvalRuns(ctx, &EvalRunFilter{SkillName: "triage-issue", IncludeCases: true})
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

// TestListEvalRuns_MissingTableIsEmpty pins the read-only-upgrade fallback: a DB
// that predates migration 0009 (tables absent, migrations skipped on a
// read-only open) yields an empty result, not a "no such table" error.
func TestListEvalRuns_MissingTableIsEmpty(t *testing.T) {
	st := openTestStore(t)
	sq, ok := st.(*sqliteStore)
	if !ok {
		t.Fatal("expected *sqliteStore")
	}
	if _, err := sq.db.Exec(`DROP TABLE eval_case_results; DROP TABLE eval_runs;`); err != nil {
		t.Fatalf("drop tables: %v", err)
	}
	got, err := st.ListEvalRuns(context.Background(), &EvalRunFilter{SkillName: "x"})
	if err != nil || got != nil {
		t.Errorf("missing table: got (%v, %v), want (nil, nil)", got, err)
	}
}

// TestDeleteSession_NullsEvalRunSession proves an eval verdict OUTLIVES the
// session it graded (durable lineage), with its now-stale session_id nulled.
func TestDeleteSession_NullsEvalRunSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()
	if err := st.ReplaceSessionDerivation(ctx, metaFor(sid, "claude-code", 1000, "guard-tests"), nil); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if _, err := st.PutEvalRun(ctx, &EvalRunRow{
		SkillName: "guard-tests", SkillCommit: "abc1234", Suite: "s",
		SessionID: sid.String(), Passed: 1, Pass: true,
	}); err != nil {
		t.Fatalf("put eval run: %v", err)
	}
	if _, err := st.DeleteSession(ctx, sid); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	runs, err := st.ListEvalRuns(ctx, &EvalRunFilter{SkillName: "guard-tests"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("eval verdict did not survive session delete: got %d", len(runs))
	}
	if runs[0].SessionID != "" {
		t.Errorf("stale session_id not cleared: %q", runs[0].SessionID)
	}
}
