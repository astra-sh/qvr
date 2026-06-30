package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SpanRow is a persisted derived span. It is the storage form of a
// derive.Span (the derive package maps to/from this so the store stays
// independent of the derive layer). Attributes is the OpenTelemetry gen_ai.*
// attribute map serialized as JSON.
type SpanRow struct {
	SpanID         string    `json:"span_id"`
	TraceID        string    `json:"trace_id"`
	ParentSpanID   string    `json:"parent_span_id,omitempty"`
	SessionID      uuid.UUID `json:"session_id"`
	AgentName      string    `json:"agent_name"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	StartMs        int64     `json:"start_ms"`
	EndMs          int64     `json:"end_ms"`
	Attributes     string    `json:"attributes"` // JSON
	DeriverVersion int       `json:"deriver_version"`
	DerivedAt      time.Time `json:"derived_at"`
}

// SpanFilter scopes QuerySpans. Nil/zero fields are ignored. The skill.* cuts
// read JSON attributes (skill.name, skill.content_hash, qvr.outcome,
// skill.activation) via json_extract — they are the evolution loop's grain.
type SpanFilter struct {
	SessionID *uuid.UUID
	Agents    []string
	Kinds     []string
	// Skills matches skill.name exactly. Versions matches skill.content_hash by
	// PREFIX (so a short hash works), OR'd across the slice. Statuses matches
	// qvr.outcome exactly (the run-status cut). Activations matches
	// skill.activation exactly ("tool" | "path").
	Skills      []string
	Versions    []string
	Statuses    []string
	Activations []string
	Limit       int
}

const insertSpanSQL = `INSERT INTO spans(
  span_id, trace_id, parent_span_id, session_id, agent_name, kind, name,
  start_ms, end_ms, attributes, deriver_version, derived_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`

// ReplaceSessionSpans atomically replaces all stored spans for a session with
// rows. Replacement (not upsert) keeps the stored set exactly in sync with the
// current derivation, even when a re-derive yields fewer spans. A nil/empty
// rows slice just clears the session's spans.
func (s *sqliteStore) ReplaceSessionSpans(ctx context.Context, sessionID uuid.UUID, rows []*SpanRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: spans tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceSessionSpansTx(ctx, tx, sessionID, rows); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceSessionSpansTx is the shared span-replacement body, run inside the
// caller's tx (ReplaceSessionSpans and ReplaceSessionDerivation).
func replaceSessionSpansTx(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID, rows []*SpanRow) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM spans WHERE session_id = ?`, sessionID.String()); err != nil {
		return fmt.Errorf("store: clear session spans: %w", err)
	}
	// Defensively drop duplicate span_ids (last write wins) before inserting:
	// span_id is UNIQUE, and a single colliding row from a deriver bug must not
	// fail the whole insert and lose the entire session's spans (#147). Derivers
	// are expected to emit unique ids; this is the belt-and-suspenders backstop.
	seen := make(map[string]struct{}, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r == nil {
			continue
		}
		if _, dup := seen[r.SpanID]; dup {
			rows[i] = nil // a later row already claimed this span_id
			continue
		}
		seen[r.SpanID] = struct{}{}
	}
	for _, r := range rows {
		if r == nil {
			continue
		}
		if r.DerivedAt.IsZero() {
			r.DerivedAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, insertSpanSQL,
			r.SpanID, r.TraceID, nullableString(r.ParentSpanID), sessionID.String(),
			r.AgentName, r.Kind, nullableString(r.Name),
			r.StartMs, r.EndMs, nullableString(r.Attributes), r.DeriverVersion, r.DerivedAt.UTC(),
		); err != nil {
			return fmt.Errorf("store: insert span: %w", err)
		}
	}
	return nil
}

const spanColumns = `span_id, trace_id, parent_span_id, session_id, agent_name,
  kind, name, start_ms, end_ms, attributes, deriver_version, derived_at`

// JSON-path expressions for the skill.* attribute cuts. Dotted keys require
// the quoted form; these mirror the writer-side attribute keys in the derive
// package (skill.content_hash = derive.SkillContentHashKey, etc.).
const (
	spanSkillNameExpr    = `json_extract(attributes, '$."skill.name"')`
	spanContentHashExpr  = `COALESCE(json_extract(attributes, '$."skill.content_hash"'), '')`
	spanOutcomeExpr      = `COALESCE(json_extract(attributes, '$."qvr.outcome"'), '')`
	spanActivationExpr   = `COALESCE(json_extract(attributes, '$."skill.activation"'), '')`
	contentHashHexOffset = 8 // 1-based: skip the "sha256:" prefix (7 chars) to a hex prefix match
)

// spanWhere builds the WHERE clauses and args for a SpanFilter.
func spanWhere(f *SpanFilter) (clauses []string, args []any) {
	if f == nil {
		return nil, nil
	}
	if f.SessionID != nil {
		clauses = append(clauses, "session_id = ?")
		args = append(args, f.SessionID.String())
	}
	add := func(expr string, vals []string) {
		if len(vals) == 0 {
			return
		}
		clauses = append(clauses, expr+" IN ("+placeholders(len(vals))+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	add("agent_name", f.Agents)
	add("kind", f.Kinds)
	add(spanSkillNameExpr, f.Skills)
	add(spanOutcomeExpr, f.Statuses)
	add(spanActivationExpr, f.Activations)
	if len(f.Versions) > 0 {
		// Match each token as a hex PREFIX of the content hash (git-style short
		// hash), OR'd across the slice.
		var or []string
		for _, v := range f.Versions {
			or = append(or, "substr("+spanContentHashExpr+", "+strconv.Itoa(contentHashHexOffset)+") LIKE ? || '%'")
			args = append(args, v)
		}
		clauses = append(clauses, "("+strings.Join(or, " OR ")+")")
	}
	return clauses, args
}

// QuerySpans returns stored spans ordered by (session_id, start_ms).
func (s *sqliteStore) QuerySpans(ctx context.Context, f *SpanFilter) ([]*SpanRow, error) {
	clauses, args := spanWhere(f)
	q := `SELECT ` + spanColumns + ` FROM spans`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY session_id ASC, start_ms ASC"
	if f != nil && f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query spans: %w", err)
	}
	defer rows.Close()

	var out []*SpanRow
	for rows.Next() {
		r, err := scanSpan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanSpan(rows interface {
	Scan(...any) error
}) (*SpanRow, error) {
	var (
		r                  SpanRow
		sid                string
		parent, name, attr sql.NullString
		derivedAt          time.Time
	)
	if err := rows.Scan(&r.SpanID, &r.TraceID, &parent, &sid, &r.AgentName,
		&r.Kind, &name, &r.StartMs, &r.EndMs, &attr, &r.DeriverVersion, &derivedAt); err != nil {
		return nil, fmt.Errorf("store: scan span: %w", err)
	}
	id, err := uuid.Parse(sid)
	if err != nil {
		return nil, fmt.Errorf("store: bad span session_id %q: %w", sid, err)
	}
	r.SessionID = id
	r.ParentSpanID = parent.String
	r.Name = name.String
	r.Attributes = attr.String
	r.DerivedAt = derivedAt.UTC()
	return &r, nil
}
