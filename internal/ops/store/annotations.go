package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AnnotationRow is one human verdict on a captured session — the outer-loop
// feedback signal. It is durable: derivation (`qvr audit rederive`) never
// rewrites it, unlike the spans and session_meta projections. A whole-session
// verdict leaves Skill empty; a per-skill verdict scopes it to one skill.
type AnnotationRow struct {
	SessionID uuid.UUID `json:"session_id"`
	Skill     string    `json:"skill,omitempty"`
	Outcome   string    `json:"outcome"` // the reviewer's verdict (e.g. good, bad, blocked)
	Note      string    `json:"note,omitempty"`
	Author    string    `json:"author,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AnnotationFilter scopes ListAnnotations. Nil/zero fields are ignored; the
// result is newest-first by created_at.
type AnnotationFilter struct {
	SessionID *uuid.UUID
	Skill     string
	Since     *time.Time
}

// PutAnnotation appends one human verdict. Annotations are append-only — a
// session can accrue several over time — so this never replaces a prior row.
func (s *sqliteStore) PutAnnotation(ctx context.Context, a *AnnotationRow) error {
	if a == nil {
		return fmt.Errorf("store: nil annotation")
	}
	if a.Outcome == "" {
		return fmt.Errorf("store: annotation outcome is required")
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO annotations(session_id, skill, outcome, note, author, created_at)
		 VALUES (?,?,?,?,?,?)`,
		a.SessionID.String(), nullableString(a.Skill), a.Outcome,
		nullableString(a.Note), nullableString(a.Author), a.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("store: put annotation: %w", err)
	}
	return nil
}

// ListAnnotations returns the verdicts matching the filter, newest-first.
func (s *sqliteStore) ListAnnotations(ctx context.Context, f *AnnotationFilter) ([]*AnnotationRow, error) {
	var where []string
	var args []any
	if f != nil {
		if f.SessionID != nil {
			where = append(where, "session_id = ?")
			args = append(args, f.SessionID.String())
		}
		if f.Skill != "" {
			where = append(where, "skill = ?")
			args = append(args, f.Skill)
		}
		if f.Since != nil {
			where = append(where, "created_at >= ?")
			args = append(args, f.Since.UTC())
		}
	}
	q := `SELECT session_id, skill, outcome, note, author, created_at FROM annotations`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list annotations: %w", err)
	}
	defer rows.Close()

	var out []*AnnotationRow
	for rows.Next() {
		a, err := scanAnnotation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAnnotation(row interface{ Scan(...any) error }) (*AnnotationRow, error) {
	var (
		a                   AnnotationRow
		sid                 string
		skill, note, author sql.NullString
		createdAt           time.Time
	)
	if err := row.Scan(&sid, &skill, &a.Outcome, &note, &author, &createdAt); err != nil {
		return nil, fmt.Errorf("store: scan annotation: %w", err)
	}
	id, err := uuid.Parse(sid)
	if err != nil {
		return nil, fmt.Errorf("store: bad annotation session_id %q: %w", sid, err)
	}
	a.SessionID = id
	a.Skill = skill.String
	a.Note = note.String
	a.Author = author.String
	a.CreatedAt = createdAt.UTC()
	return &a, nil
}
