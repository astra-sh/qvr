package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// PutScore records a BYO grader's verdict for one run of one skill, keyed by the
// agent-native session id and the skill name (see migrations 0010, 0011). The
// grade asserts "this SKILL's run scored X", not "this session scored X": a
// session can load several skills, so without the skill key one grade would
// double-count across every skill the session touched. It is a BLIND write: it
// never checks that the session has been discovered, so a grader can score
// immediately after a run and the rollup picks it up whenever the session is
// ingested (grade-first, discover-later — no ordering dependency). INSERT OR
// REPLACE so re-grading the same (agent, session, skill, metric) updates in place.
//
// agent, sessionRef, and skill are lowercased to match the rollup's join, which
// lowercases session_meta.source_session_id and the span's skill.name — claude
// stores the session id uppercase while the canonical id is lowercase, so without
// normalization a grade would silently miss.
func (s *sqliteStore) PutScore(ctx context.Context, agent, sessionRef, skill, metric string, value float64, grader string) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	sessionRef = strings.ToLower(strings.TrimSpace(sessionRef))
	skill = strings.ToLower(strings.TrimSpace(skill))
	if agent == "" || sessionRef == "" || skill == "" {
		return fmt.Errorf("store: put score: agent, session id, and skill are required")
	}
	if metric == "" {
		metric = "score"
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO session_score
		(agent, session_ref, skill, metric, value, grader, scored_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		agent, sessionRef, skill, metric, value, grader, time.Now().UTC().UnixMilli()); err != nil {
		return fmt.Errorf("store: put score: %w", err)
	}
	return nil
}

// SessionSkillLoaded reports whether the (agent, sessionRef) run has been
// discovered yet (known) and, if so, whether it loaded skill as a genuine SKILL
// span (loaded). It backs annotate's guard: a grade naming a skill the run never
// loaded can't attribute (the rollup join below would drop it), so the caller can
// flag or refuse it. An undiscovered run is known=false — no opinion, so
// grade-first/discover-later stays a blind write.
func (s *sqliteStore) SessionSkillLoaded(ctx context.Context, agent, sessionRef, skill string) (known, loaded bool, err error) {
	agent = strings.ToLower(strings.TrimSpace(agent))
	sessionRef = strings.ToLower(strings.TrimSpace(sessionRef))
	skill = strings.ToLower(strings.TrimSpace(skill))
	// session_meta has no attributes column, so skillNameExpr's bare reference
	// binds unambiguously to spans in the joined EXISTS.
	row := s.db.QueryRowContext(ctx, `SELECT
	  EXISTS(SELECT 1 FROM session_meta m
	         WHERE m.agent_name = ? AND LOWER(m.source_session_id) = ?),
	  EXISTS(SELECT 1 FROM spans sp
	         JOIN session_meta m ON m.session_id = sp.session_id
	         WHERE m.agent_name = ? AND LOWER(m.source_session_id) = ?
	           AND sp.kind = 'SKILL' AND LOWER(`+skillNameExpr+`) = ?)`,
		agent, sessionRef, agent, sessionRef, skill)
	if err := row.Scan(&known, &loaded); err != nil {
		return false, false, fmt.Errorf("store: session skill loaded: %w", err)
	}
	return known, loaded, nil
}

// SkillScoreMetrics returns the distinct metric names skill carries grades under
// (migration 0011 keys grades by skill), sorted. It backs compare's metric-mismatch
// hint: when the requested --metric has no grades but others do, every SCORE reads
// '—' for a reason the operator can't see, and this lists the metrics that would
// actually populate. skill is lowercased to match PutScore's stored key; metric
// values are returned verbatim, since contentScores joins on them case-sensitively.
// A pre-0010 DB (no session_score table) degrades to an empty list, the same
// disposition contentScores takes — the hint simply stays silent.
func (s *sqliteStore) SkillScoreMetrics(ctx context.Context, skill string) ([]string, error) {
	skill = strings.ToLower(strings.TrimSpace(skill))
	if skill == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT metric FROM session_score
		WHERE skill = ? ORDER BY metric`, skill)
	if err != nil {
		if missingScoreSchema(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: skill score metrics: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("store: scan skill score metric: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SessionScore is one BYO-grader verdict attached to a session, joined back from
// the session_score table by the session's agent + native id. A session can carry
// several grades — one per (skill, metric) it was annotated under — so consumers
// read this as a list. Value is the grader's raw number (e.g. a 0/1 pass or a
// 0..1 pass-rate), Grader the grader id, ScoredAt the epoch-ms write time. The
// metric named "score" is the one compare's cohort rollup aggregates; other
// metrics are surfaced verbatim but do not roll up there.
type SessionScore struct {
	Skill    string  `json:"skill"`
	Metric   string  `json:"metric"`
	Value    float64 `json:"value"`
	Grader   string  `json:"grader,omitempty"`
	ScoredAt int64   `json:"scored_at"`
}

// ScoresForSessions returns the BYO-grader verdicts attached to each of the given
// sessions, keyed by session id string. It joins session_score back through
// session_meta on (agent, LOWER(source_session_id)) — the SAME normalized key the
// cohort rollup uses (contentScores), so a grade written against the agent-native
// id matches regardless of the canonical session_id. The LOWER() join is what
// makes codex grades (whose native id differs from the canonical one) and claude's
// uppercase-stored ids resolve; without it they would silently orphan.
//
// A pre-0010 DB (read-only open before the session_score table exists) degrades to
// an empty map rather than failing the session view — the same graceful fallback
// contentScores takes. Sessions with no grade are simply absent from the map.
func (s *sqliteStore) ScoresForSessions(ctx context.Context, ids []string) (map[string][]SessionScore, error) {
	out := map[string][]SessionScore{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	// Alias the reserved-ish columns so Scan reads positionally; join key matches
	// contentScores (agent + lowercased source_session_id).
	q := `SELECT m.session_id, sc.skill, sc.metric, sc.value, sc.grader, sc.scored_at
	      FROM session_meta m
	      JOIN session_score sc
	        ON sc.agent = m.agent_name
	       AND sc.session_ref = LOWER(m.source_session_id)
	      WHERE m.session_id IN (` + placeholders(len(ids)) + `)
	      ORDER BY m.session_id, sc.skill, sc.metric`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		if missingScoreSchema(err) {
			return out, nil
		}
		return nil, fmt.Errorf("store: scores for sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sid string
		var sc SessionScore
		if err := rows.Scan(&sid, &sc.Skill, &sc.Metric, &sc.Value, &sc.Grader, &sc.ScoredAt); err != nil {
			return nil, fmt.Errorf("store: scan session score: %w", err)
		}
		out[sid] = append(out[sid], sc)
	}
	return out, rows.Err()
}

// scoreAgg is one cohort's score totals: how many of its runs were graded (the
// honest denominator) and the sum of their values (mean = sum/count).
type scoreAgg struct {
	count int64
	sum   float64
}

// cohortKey is the merge key the content rollup joins its scores and tokens on:
// the content coordinate alone when agents collapse, or content+agent under
// f.ByAgent so each {version x agent} cell gets its own grade and cost. The unit
// separator (0x1f) can't occur in a content hash or an agent name.
func cohortKey(chash, agent string) string {
	if agent == "" {
		return chash
	}
	return chash + "\x1f" + agent
}

// cohortAgentSQL returns the (selected column, GROUP BY suffix) that splits a
// content rollup's side queries by agent under f.ByAgent, mirroring the constant
// ” the main rollup selects when agents collapse — so the scanned column count
// stays fixed and the returned key lines up with the cohort's.
func cohortAgentSQL(f *MetricsFilter) (col, group string) {
	if f != nil && f.ByAgent {
		return "m.agent_name", ", m.agent_name"
	}
	return "'' AS agent", ""
}

// contentScores returns, per content coordinate, the graded-run count and value
// sum for the given metric — the substrate the compare rollup folds into each
// cohort. It reduces spans to (content coordinate, session, skill) grain FIRST,
// then inner-joins the score table through session_meta.source_session_id AND the
// skill name (both lowercased on both sides), so a grade contributes only to the
// skill it judged. The skill match is what stops a multi-skill session's single
// grade from double-counting across every cohort the session touched — the score
// is session-weighted within its skill, not span-weighted.
//
// A pre-0010 DB (read-only open, migration not yet applied) has no session_score
// table; that degrades to "no scores" rather than failing the whole rollup, the
// same graceful-fallback discipline the token rollup uses for pre-0006 schemas.
func (s *sqliteStore) contentScores(ctx context.Context, f *MetricsFilter) (map[string]*scoreAgg, error) {
	metric := "score"
	if f != nil && f.Metric != "" {
		metric = f.Metric
	}
	clauses, args := skillSpanWhere(f)
	agentCol, agentGroup := cohortAgentSQL(f)
	q := `WITH runs AS (
	  SELECT DISTINCT ` + skillVersionCoordExpr + ` AS chash, session_id, LOWER(` + skillNameExpr + `) AS skill_name
	  FROM spans WHERE ` + strings.Join(clauses, " AND ") + `
	)
	SELECT runs.chash, ` + agentCol + `, COUNT(sc.value), COALESCE(SUM(sc.value), 0)
	FROM runs
	JOIN session_meta  m  ON m.session_id  = runs.session_id
	JOIN session_score sc ON sc.agent = m.agent_name
	   AND sc.session_ref = LOWER(m.source_session_id)
	   AND sc.skill = runs.skill_name
	   AND sc.metric = ?
	GROUP BY runs.chash` + agentGroup
	args = append(args, metric)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		if missingScoreSchema(err) {
			return map[string]*scoreAgg{}, nil
		}
		return nil, fmt.Errorf("store: content scores: %w", err)
	}
	defer rows.Close()
	out := map[string]*scoreAgg{}
	for rows.Next() {
		var chash, agent string
		a := &scoreAgg{}
		if err := rows.Scan(&chash, &agent, &a.count, &a.sum); err != nil {
			return nil, fmt.Errorf("store: scan content score: %w", err)
		}
		out[cohortKey(chash, agent)] = a
	}
	return out, rows.Err()
}

// verCohortKey is the (ref, commit) merge key the lineage rollup folds its
// per-version scores on. The unit separator (0x1f) can't occur in a ref or sha.
func verCohortKey(ref, commit string) string {
	return ref + "\x1f" + commit
}

// versionScores returns, per (ref, commit) version coordinate, the graded-run
// count and value sum for the metric — the score half of the lineage rollup, the
// git-coordinate sibling of contentScores. It reduces SKILL spans to
// (ref, commit, session, skill) grain FIRST, then inner-joins session_score
// through session_meta on the SAME (agent, LOWER(source_session_id)) key and the
// lowercased skill name, so a grade contributes only to the version coordinate the
// graded session actually ran. A graded session counts toward the unknown (ref=”)
// coordinate only when none of its spans carried a proven ref — the same guard
// SkillVersionRollup's token CTE uses — so it never double-counts under both its
// proven row and the unknown row.
//
// A pre-0010 DB has no session_score table; that degrades to "no scores" rather
// than failing the rollup, matching contentScores.
func (s *sqliteStore) versionScores(ctx context.Context, f *MetricsFilter) (map[string]*scoreAgg, error) {
	metric := "score"
	if f != nil && f.Metric != "" {
		metric = f.Metric
	}
	clauses, args := skillSpanWhere(f)
	q := `WITH ver AS (
	  SELECT DISTINCT ` + skillVersionExpr + ` AS ref,
	         ` + skillCommitExpr + ` AS commit_sha,
	         session_id, LOWER(` + skillNameExpr + `) AS skill_name
	  FROM spans WHERE ` + strings.Join(clauses, " AND ") + `
	)
	SELECT ver.ref, ver.commit_sha, COUNT(sc.value), COALESCE(SUM(sc.value), 0)
	FROM ver
	JOIN session_meta  m  ON m.session_id  = ver.session_id
	JOIN session_score sc ON sc.agent = m.agent_name
	   AND sc.session_ref = LOWER(m.source_session_id)
	   AND sc.skill = ver.skill_name
	   AND sc.metric = ?
	WHERE ver.ref <> ''
	   OR NOT EXISTS (SELECT 1 FROM ver pv
	                  WHERE pv.session_id = ver.session_id AND pv.ref <> '')
	GROUP BY ver.ref, ver.commit_sha`
	args = append(args, metric)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		if missingScoreSchema(err) {
			return map[string]*scoreAgg{}, nil
		}
		return nil, fmt.Errorf("store: version scores: %w", err)
	}
	defer rows.Close()
	out := map[string]*scoreAgg{}
	for rows.Next() {
		var ref, commit string
		a := &scoreAgg{}
		if err := rows.Scan(&ref, &commit, &a.count, &a.sum); err != nil {
			return nil, fmt.Errorf("store: scan version score: %w", err)
		}
		out[verCohortKey(ref, commit)] = a
	}
	return out, rows.Err()
}

// tokenAgg is one cohort's session-attributed token cost: nil sides mean the
// cohort's sessions reported no usage there (n/a), never a fabricated 0.
type tokenAgg struct {
	in  *int64
	out *int64
}

// contentTokens returns, per content coordinate (split by agent under f.ByAgent),
// the token cost over the sessions the cohort's runs fired in — the cost half of
// the rollup's (quality, cost) frontier. Like contentScores it reduces spans to
// (coordinate[, agent], session) grain FIRST, then sums session_meta token totals,
// so a session's tokens count once per cohort. This is session-attributed
// EXPOSURE, not exclusive cost: a session that fired two skills lends its tokens
// to both (see the attribution note atop metrics.go). Token-less agents keep nil
// sides — the same NULL-preservation the per-skill token rollup guarantees, so a
// cohort with no usage data never poisons a cross-agent cost comparison.
//
// A pre-0006 DB lacks the token columns; queryTokenRollup degrades it gracefully,
// the same fallback SkillTokenRollup relies on.
func (s *sqliteStore) contentTokens(ctx context.Context, f *MetricsFilter) (map[string]*tokenAgg, error) {
	clauses, args := skillSpanWhere(f)
	agentCol, agentGroup := cohortAgentSQL(f)
	q := `WITH runs AS (
	  SELECT DISTINCT ` + skillVersionCoordExpr + ` AS chash, session_id
	  FROM spans WHERE ` + strings.Join(clauses, " AND ") + `
	)
	SELECT runs.chash, ` + agentCol + `,
	  CAST(SUM(m.tokens_in) AS INTEGER),
	  CAST(SUM(m.tokens_out) AS INTEGER)
	FROM runs
	JOIN session_meta m ON m.session_id = runs.session_id
	GROUP BY runs.chash` + agentGroup

	rows, err := s.queryTokenRollup(ctx, q, args)
	if err != nil {
		return nil, fmt.Errorf("store: content tokens: %w", err)
	}
	defer rows.Close()
	out := map[string]*tokenAgg{}
	for rows.Next() {
		var chash, agent string
		var tin, tout sql.NullInt64
		if err := rows.Scan(&chash, &agent, &tin, &tout); err != nil {
			return nil, fmt.Errorf("store: scan content tokens: %w", err)
		}
		out[cohortKey(chash, agent)] = &tokenAgg{in: nullInt64Ptr(tin), out: nullInt64Ptr(tout)}
	}
	return out, rows.Err()
}

// missingScoreSchema reports the pre-0010 "session_score table missing" condition.
func missingScoreSchema(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: session_score")
}
