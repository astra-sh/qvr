package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EvalRunRow is one recorded eval run, keyed by {skill_name, skill_commit} so it
// joins to spans/lineage by the exact locked commit that ran. Cases carry the
// per-case verdicts; they are written with the run and loaded on demand.
type EvalRunRow struct {
	ID          int64         `json:"id"`
	SkillName   string        `json:"skill_name"`
	SkillCommit string        `json:"skill_commit"`
	Suite       string        `json:"suite"`
	SessionID   string        `json:"session_id,omitempty"`
	StartedAt   time.Time     `json:"started_at"`
	Passed      int           `json:"passed"`
	Failed      int           `json:"failed"`
	Pass        bool          `json:"pass"`
	Cases       []EvalCaseRow `json:"cases,omitempty"`
}

// EvalCaseRow is one case's verdict within a run.
type EvalCaseRow struct {
	Suite  string `json:"suite"`
	Case   string `json:"case"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// EvalRunFilter scopes ListEvalRuns. Nil/zero fields are ignored; results are
// newest-first by started_at.
type EvalRunFilter struct {
	SkillName   string
	SkillCommit string
	Since       *time.Time
	Limit       int
}

// PutEvalRun inserts a run and its case rows in one tx, returning the new run id.
func (s *sqliteStore) PutEvalRun(ctx context.Context, r *EvalRunRow) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("store: nil eval run")
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: eval run tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO eval_runs(skill_name, skill_commit, suite, session_id, started_at, passed, failed, pass)
		 VALUES (?,?,?,?,?,?,?,?)`,
		r.SkillName, r.SkillCommit, r.Suite, nullableString(r.SessionID),
		r.StartedAt.UTC(), r.Passed, r.Failed, boolToInt(r.Pass))
	if err != nil {
		return 0, fmt.Errorf("store: insert eval run: %w", err)
	}
	id, _ := res.LastInsertId()
	for _, c := range r.Cases {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO eval_case_results(eval_run_id, suite, case_name, pass, detail)
			 VALUES (?,?,?,?,?)`,
			id, c.Suite, c.Case, boolToInt(c.Pass), nullableString(c.Detail)); err != nil {
			return 0, fmt.Errorf("store: insert eval case: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: eval run commit: %w", err)
	}
	r.ID = id
	return id, nil
}

// ListEvalRuns returns runs matching the filter, newest-first, each with its
// case rows loaded.
func (s *sqliteStore) ListEvalRuns(ctx context.Context, f *EvalRunFilter) ([]*EvalRunRow, error) {
	var where []string
	var args []any
	if f != nil {
		if f.SkillName != "" {
			where = append(where, "skill_name = ?")
			args = append(args, f.SkillName)
		}
		if f.SkillCommit != "" {
			where = append(where, "skill_commit = ?")
			args = append(args, f.SkillCommit)
		}
		if f.Since != nil {
			where = append(where, "started_at >= ?")
			args = append(args, f.Since.UTC())
		}
	}
	q := `SELECT id, skill_name, skill_commit, suite, session_id, started_at, passed, failed, pass FROM eval_runs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY started_at DESC"
	if f != nil && f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list eval runs: %w", err)
	}
	defer rows.Close()

	var out []*EvalRunRow
	for rows.Next() {
		r, err := scanEvalRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range out {
		cases, err := s.evalCases(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		r.Cases = cases
	}
	return out, nil
}

func (s *sqliteStore) evalCases(ctx context.Context, runID int64) ([]EvalCaseRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT suite, case_name, pass, detail FROM eval_case_results WHERE eval_run_id = ?`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list eval cases: %w", err)
	}
	defer rows.Close()
	var out []EvalCaseRow
	for rows.Next() {
		var (
			c      EvalCaseRow
			p      int
			detail sql.NullString
		)
		if err := rows.Scan(&c.Suite, &c.Case, &p, &detail); err != nil {
			return nil, fmt.Errorf("store: scan eval case: %w", err)
		}
		c.Pass = p != 0
		c.Detail = detail.String
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanEvalRun(row interface{ Scan(...any) error }) (*EvalRunRow, error) {
	var (
		r         EvalRunRow
		sessionID sql.NullString
		passInt   int
		startedAt time.Time
	)
	if err := row.Scan(&r.ID, &r.SkillName, &r.SkillCommit, &r.Suite, &sessionID,
		&startedAt, &r.Passed, &r.Failed, &passInt); err != nil {
		return nil, fmt.Errorf("store: scan eval run: %w", err)
	}
	r.SessionID = sessionID.String
	r.StartedAt = startedAt.UTC()
	r.Pass = passInt != 0
	return &r, nil
}
