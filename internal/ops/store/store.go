// Package store is the SQLite-backed persistence layer for SkillOps.
// It is a thin seam over database/sql — no ORM, no codegen, no magic.
// Schema lives in migrations/; raw read/write logic lives in raw.go and
// raw_sessions.go; row-scanning helpers live in scan.go.
//
// The store is raw-only: capture writes verbatim transcript lines and hook
// payloads, and every read surface is derived from those rows (or from the
// derive layer). There is no normalized event/session table.
package store

import (
	"context"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/google/uuid"
)

// Store is the raw-only persistence contract. Every method takes ctx so
// sweeps and long-running exports can be cancelled.
type Store interface {
	// --- Capture (canonical write path) ---

	// AppendRawTraces stores verbatim agent output (transcript lines and/or
	// hook payloads) and advances the per-file tailing cursor in one tx.
	AppendRawTraces(ctx context.Context, rows []*ops.RawTrace, cursor *RawCursor) error

	// GetRawCursor returns the byte offset capture last consumed for a
	// transcript file, or 0 if it has never been tailed.
	GetRawCursor(ctx context.Context, agent, sourcePath string) (int64, error)

	// --- Reads over raw rows ---

	// QueryRawTraces returns raw rows ordered by (session_id, seq) ascending.
	QueryRawTraces(ctx context.Context, f *RawTraceFilter) ([]*ops.RawTrace, error)

	// StreamRawTraces calls fn per matching row, ordered by (session_id, seq).
	StreamRawTraces(ctx context.Context, f *RawTraceFilter, fn func(*ops.RawTrace) error) error

	// ListRawSessions returns per-session summaries derived from raw rows,
	// newest-first by first-seen time.
	ListRawSessions(ctx context.Context, f *RawSessionFilter) ([]*RawSession, error)

	// CountRawSessions / CountRawTraces count distinct sessions / rows,
	// optionally scoped to working dirs and/or one agent (empty = all).
	CountRawSessions(ctx context.Context, dirs []string, agent string) (int64, error)
	CountRawTraces(ctx context.Context, dirs []string, agent string) (int64, error)

	// LatestRawAt returns the newest capture time for an agent, or nil.
	LatestRawAt(ctx context.Context, agent string) (*time.Time, error)

	// DistinctRawAgents returns every agent name present in raw_traces, sorted.
	DistinctRawAgents(ctx context.Context) ([]string, error)

	// --- Derived projections (regenerable; see session_meta.go / spans.go) ---

	// ReplaceSessionDerivation atomically replaces a session's whole derived
	// projection — its unified session_meta row and all its spans — in one tx.
	ReplaceSessionDerivation(ctx context.Context, meta *SessionMetaRow, rows []*SpanRow) error

	// ListSessionMeta returns unified session rows newest-first by start time.
	ListSessionMeta(ctx context.Context, f *SessionMetaFilter) ([]*SessionMetaRow, error)

	// GetSessionMeta returns one session's unified row, or nil when absent.
	GetSessionMeta(ctx context.Context, sessionID uuid.UUID) (*SessionMetaRow, error)

	// ReplaceSessionSpans atomically replaces a session's stored spans with
	// the given rows (the result of re-deriving that session).
	ReplaceSessionSpans(ctx context.Context, sessionID uuid.UUID, rows []*SpanRow) error

	// QuerySpans returns stored spans ordered by (session_id, start_ms).
	QuerySpans(ctx context.Context, f *SpanFilter) ([]*SpanRow, error)

	// SkillsForSessions returns the distinct skill.name values attributed to
	// each given session (from its SKILL-attributed spans), keyed by session id
	// string. Sessions with no skill span are absent from the map.
	SkillsForSessions(ctx context.Context, ids []string) (map[string][]string, error)

	// SkillVersionsForSessions returns each session's per-skill version
	// coordinate (ref/commit/subtree) from its persisted SKILL spans, so a
	// consumer learns which version ran without a time-window cross-reference.
	SkillVersionsForSessions(ctx context.Context, ids []string) (map[string][]SkillVersionCoord, error)

	// ScoresForSessions returns the BYO-grader verdicts attached to each given
	// session, keyed by session id string. It joins session_score on the same
	// (agent, LOWER(source_session_id)) key the cohort rollup uses, so a grade
	// written against the agent-native id resolves for codex (differing id) and
	// claude (uppercase id) alike. Pre-0010 DBs degrade to an empty map.
	ScoresForSessions(ctx context.Context, ids []string) (map[string][]SessionScore, error)

	// IdentityForCommit returns the full identity any session already proved for
	// a (short) commit, or nil — the lock-independent source for escalating a
	// commit-only span to its proper ref/subtree regardless of the current checkout.
	IdentityForCommit(ctx context.Context, commit string) (*SkillCommitIdentity, error)

	// IdentityForContentHash is the body-digest counterpart of IdentityForCommit,
	// for a claude run whose symlink-recorded load carries no commit — it inherits
	// the identity another session proved for the same run-time content.
	IdentityForContentHash(ctx context.Context, contentHash string) (*SkillCommitIdentity, error)

	// --- Activity analytics (read-side aggregations over session_meta and
	// the scan ledger; see activity.go) ---

	// ActivitySummary aggregates headline totals + per-agent slices.
	ActivitySummary(ctx context.Context, f *ActivityFilter) (*ActivitySummary, error)

	// ActivitySeries returns per-day per-agent buckets, oldest first.
	ActivitySeries(ctx context.Context, f *ActivityFilter) ([]*ActivityBucket, error)

	// SkippedSkilllessSeries counts scan-skipped skill-less sessions per
	// (day, agent) from the scan ledger (machine-global; never project-scoped).
	SkippedSkilllessSeries(ctx context.Context, since, until *time.Time) ([]*SkippedBucket, error)

	// --- Skill metrics (read-side aggregations over spans; see metrics.go) ---

	// SkillUsageRollup aggregates SKILL spans per skill (invocations, sessions,
	// observed versions, first/last fired), most-recently-fired first.
	SkillUsageRollup(ctx context.Context, f *MetricsFilter) ([]*SkillUsage, error)

	// SkillTokenRollup returns per-skill session-attributed token totals,
	// keyed by skill name ("tokens in sessions where this skill fired").
	SkillTokenRollup(ctx context.Context, f *MetricsFilter) (map[string]*TokenTotals, error)

	// SkillSessionDurationRollup returns per-skill session-attributed wall-clock
	// duration, keyed by skill name ("how long the sessions this skill fired in
	// ran"). Exposure, not exclusive.
	SkillSessionDurationRollup(ctx context.Context, f *MetricsFilter) (map[string]*DurationStats, error)

	// SkillInvocationSeries buckets one skill's invocations by UTC day and
	// agent. f.Skill is required.
	SkillInvocationSeries(ctx context.Context, f *MetricsFilter) ([]*SkillSeriesPoint, error)

	// SkillAgentRollup aggregates one skill's invocations per agent,
	// including session-attributed token totals (nil = no usage reported).
	// f.Skill is required.
	SkillAgentRollup(ctx context.Context, f *MetricsFilter) ([]*SkillAgentUsage, error)

	// SkillModelRollup aggregates one skill's invocations per model — the
	// skill × model performance cut, including session-attributed token
	// totals (models overlap: a two-model session counts toward both).
	// f.Skill is required.
	SkillModelRollup(ctx context.Context, f *MetricsFilter) ([]*SkillModelUsage, error)

	// SkillVersionRollup groups one skill's invocations by the (ref, commit)
	// identity its spans carried — the lineage data. f.Skill is required.
	SkillVersionRollup(ctx context.Context, f *MetricsFilter) ([]*SkillVersionUsage, error)

	// SkillContentRollup groups one skill's invocations by observed content
	// hash (the evolution loop's comparison coordinate), with a run-status
	// breakdown per cohort. f.Skill is required; f.Activation scopes the cut.
	SkillContentRollup(ctx context.Context, f *MetricsFilter) ([]*SkillContentCohort, error)

	// PutLockSnapshots freezes a session's ingest-time proven identities
	// (write-once per session+skill). GetLockSnapshots reads them back keyed
	// by skill name. See migration 0005.
	PutLockSnapshots(ctx context.Context, sessionID uuid.UUID, rows []*LockSnapshotRow) error
	GetLockSnapshots(ctx context.Context, sessionID uuid.UUID) (map[string]*LockSnapshotRow, error)

	// PutScore records a BYO grader's verdict for one run of one skill, keyed by
	// the agent-native session id and the skill name (a blind write — no
	// session-existence check, so grading can precede discovery). The skill key
	// scopes the grade to the skill it judged, so a multi-skill session's grade
	// no longer double-counts. See migrations 0010/0011; the score folds into
	// SkillContentRollup's cohorts as MeanScore/Graded.
	PutScore(ctx context.Context, agent, sessionRef, skill, metric string, value float64, grader string) error

	// SessionSkillLoaded reports whether (agent, sessionRef) has been discovered
	// (known) and whether it loaded skill as a genuine SKILL span (loaded). Backs
	// annotate's guard against a grade naming a skill the run never loaded; an
	// undiscovered run is known=false (no opinion, preserving grade-first).
	SessionSkillLoaded(ctx context.Context, agent, sessionRef, skill string) (known, loaded bool, err error)

	// SkillScoreMetrics returns the distinct metric names a skill carries grades
	// under (sorted). Backs compare's metric-mismatch hint, which warns when the
	// requested --metric has no grades but other metrics do — the silent cause of
	// an all-'—' SCORE column.
	SkillScoreMetrics(ctx context.Context, skill string) ([]string, error)

	// DeleteRawBefore sweeps raw rows captured before cutoff. Returns the
	// number of rows deleted.
	DeleteRawBefore(ctx context.Context, cutoff time.Time) (int64, error)

	// DeleteSession removes a whole session — its raw rows, derived spans,
	// session_meta row, and tailing cursor — in one tx. Returns the number of
	// raw rows deleted. Used by the skill-only retention policy to drop
	// sessions with no skill usage; deleting the cursor means a session that
	// later resumes re-tails from the start and is re-captured in full. The
	// scanned_files ledger is deliberately NOT touched: it is keyed by file,
	// and a pruned file must not be re-examined while unchanged.
	DeleteSession(ctx context.Context, sessionID uuid.UUID) (int64, error)

	// --- Discovery scan ledger (see scanned_files.go) ---

	// GetScannedFiles returns one agent's whole scan ledger, keyed by path.
	GetScannedFiles(ctx context.Context, agent string) (map[string]*ScannedFile, error)

	// UpsertScannedFile records (or refreshes) one file's ledger row.
	UpsertScannedFile(ctx context.Context, f *ScannedFile) error

	// DeleteRawBySourcePath removes every raw row ingested from one source file.
	DeleteRawBySourcePath(ctx context.Context, agent, sourcePath string) (int64, error)

	// ReplaceSourceRawTraces atomically replaces one source file's raw rows
	// (the document-layout re-ingest primitive: delete + insert in one tx).
	ReplaceSourceRawTraces(ctx context.Context, agent, sourcePath string, rows []*ops.RawTrace) error

	// Stats returns counts and DB size, suitable for `qvr audit db stats`.
	Stats(ctx context.Context) (*StoreStats, error)

	// Close releases the underlying database handle. Idempotent.
	Close() error
}

// StoreStats summarises DB contents for diagnostics.
type StoreStats struct {
	RawTraceCount int64
	SessionCount  int64
	DBSizeBytes   int64
	OldestTrace   *time.Time
	NewestTrace   *time.Time
}
