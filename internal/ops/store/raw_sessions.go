package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// RawSession is a per-session summary computed on the fly from raw_traces.
// There is no sessions table in the raw-only model — a "session" is just the
// set of rows sharing a session_id, so its boundaries and counts are derived.
type RawSession struct {
	SessionID        uuid.UUID `json:"session_id"`
	AgentName        string    `json:"agent_name"`
	AgentSessionID   string    `json:"agent_session_id,omitempty"`
	WorkingDirectory string    `json:"working_directory,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	LastAt           time.Time `json:"last_at"`
	TranscriptLines  int64     `json:"transcript_lines"`
	HookPayloads     int64     `json:"hook_payloads"`
	TotalRows        int64     `json:"total_rows"`
}

// RawSessionFilter scopes ListRawSessions. Nil/zero fields are ignored.
type RawSessionFilter struct {
	Since *time.Time // sessions whose first row is at/after this time
	Agent string
	Dirs  []string // working_directory ∈ Dirs (empty = all)
	Limit int
}

const rawSessionSelect = `SELECT
  session_id,
  MAX(agent_name),
  MAX(COALESCE(agent_session_id,'')),
  MAX(COALESCE(working_directory,'')),
  MIN(captured_at),
  MAX(captured_at),
  SUM(CASE WHEN source='transcript'   THEN 1 ELSE 0 END),
  SUM(CASE WHEN source='hook_payload' THEN 1 ELSE 0 END),
  COUNT(*)
FROM raw_traces`

// ListRawSessions returns session summaries newest-first (by first-seen time).
func (s *sqliteStore) ListRawSessions(ctx context.Context, f *RawSessionFilter) ([]*RawSession, error) {
	var where []string
	var args []any
	if f != nil {
		if f.Agent != "" {
			where = append(where, "agent_name = ?")
			args = append(args, f.Agent)
		}
		if len(f.Dirs) > 0 {
			where = append(where, "working_directory IN ("+placeholders(len(f.Dirs))+")")
			for _, d := range f.Dirs {
				args = append(args, d)
			}
		}
	}
	q := rawSessionSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " GROUP BY session_id"
	if f != nil && f.Since != nil {
		q += " HAVING MIN(captured_at) >= ?"
		args = append(args, f.Since.UTC())
	}
	q += " ORDER BY MIN(captured_at) DESC"
	if f != nil && f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list raw sessions: %w", err)
	}
	defer rows.Close()

	var out []*RawSession
	for rows.Next() {
		var (
			sid, agent, agentSID, wd string
			startedStr, lastStr      string
			rs                       RawSession
		)
		if err := rows.Scan(&sid, &agent, &agentSID, &wd, &startedStr, &lastStr,
			&rs.TranscriptLines, &rs.HookPayloads, &rs.TotalRows); err != nil {
			return nil, fmt.Errorf("store: scan raw session: %w", err)
		}
		id, err := uuid.Parse(sid)
		if err != nil {
			continue // skip a non-uuid session key rather than failing the list
		}
		rs.SessionID = id
		rs.AgentName = agent
		rs.AgentSessionID = agentSID
		rs.WorkingDirectory = wd
		if t, err := parseSQLiteTime(startedStr); err == nil {
			rs.StartedAt = t
		}
		if t, err := parseSQLiteTime(lastStr); err == nil {
			rs.LastAt = t
		}
		out = append(out, &rs)
	}
	return out, rows.Err()
}

// CountRawSessions counts distinct sessions, optionally scoped to dirs/agent.
func (s *sqliteStore) CountRawSessions(ctx context.Context, dirs []string, agent string) (int64, error) {
	where, args := rawScopeWhere(dirs, agent)
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT session_id) FROM raw_traces `+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count raw sessions: %w", err)
	}
	return n, nil
}

// CountRawTraces counts rows, optionally scoped to dirs/agent.
func (s *sqliteStore) CountRawTraces(ctx context.Context, dirs []string, agent string) (int64, error) {
	where, args := rawScopeWhere(dirs, agent)
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM raw_traces `+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count raw traces: %w", err)
	}
	return n, nil
}

// LatestRawAt returns the newest capture time for an agent, or nil if none.
func (s *sqliteStore) LatestRawAt(ctx context.Context, agent string) (*time.Time, error) {
	where, args := rawScopeWhere(nil, agent)
	var raw any
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(captured_at) FROM raw_traces `+where, args...).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("store: latest raw: %w", err)
	}
	switch v := raw.(type) {
	case time.Time:
		t := v.UTC()
		return &t, nil
	case string:
		if t, err := parseSQLiteTime(v); err == nil {
			return &t, nil
		}
	}
	return nil, nil
}

// rawScopeWhere builds an optional WHERE for dirs/agent scoping.
func rawScopeWhere(dirs []string, agent string) (string, []any) {
	var clauses []string
	var args []any
	if agent != "" {
		clauses = append(clauses, "agent_name = ?")
		args = append(args, agent)
	}
	if len(dirs) > 0 {
		clauses = append(clauses, "working_directory IN ("+placeholders(len(dirs))+")")
		for _, d := range dirs {
			args = append(args, d)
		}
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// StreamRawTraces calls fn for each row matching f, ordered by (session_id,
// seq). Used by export. Returns on the first non-nil fn error.
func (s *sqliteStore) StreamRawTraces(ctx context.Context, f *RawTraceFilter, fn func(*ops.RawTrace) error) error {
	where, args := f.build()
	q := `SELECT ` + rawTraceColumns + ` FROM raw_traces ` + where +
		` ORDER BY session_id ASC, seq ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("store: stream raw traces: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRawTrace(rows)
		if err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
