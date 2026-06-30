package discover

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/ops/rawtrace"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// Store is the persistence surface a scan needs (interface-in-consumer): the
// scan ledger plus everything rawtrace.Ingest uses underneath.
type Store interface {
	rawtrace.Store
	GetScannedFiles(ctx context.Context, agent string) (map[string]*store.ScannedFile, error)
	UpsertScannedFile(ctx context.Context, f *store.ScannedFile) error
}

// Options tunes one discovery scan.
type Options struct {
	Agents []string  // restrict to these canonical agent names (empty = all scannable)
	Since  time.Time // skip files last modified before this (zero = no cutoff)
	// KeepAll disables the skill gate: every discovered session is ingested,
	// matching explicit `qvr audit ingest` semantics.
	KeepAll bool
	// DryRun walks and stat-diffs but persists nothing — no rows, no ledger.
	DryRun bool
	// Cwd scopes the scan to sessions whose recorded working directory is at or
	// under this path — so capturing one project's cohort can't pull in stray
	// sessions from elsewhere. A non-matching (or cwd-less) file is skipped
	// WITHOUT a ledger entry, so a later unscoped scan still picks it up.
	Cwd string
}

// AgentReport is one agent's scan outcome.
type AgentReport struct {
	Agent     string `json:"agent"`
	Seen      int    `json:"seen"`      // matching files found on disk
	Unchanged int    `json:"unchanged"` // skipped by the stat ledger
	Ingested  int    `json:"ingested"`  // files whose session was (re)ingested
	Skipped   int    `json:"skipped"`   // skill-gate skips (provably skill-less)
	Errors    int    `json:"errors"`
	Lines     int    `json:"lines"` // raw rows stored
	Spans     int    `json:"spans"`
	// WouldExamine counts the new/changed files a dry run would have ingested
	// (kept distinct from Ingested, which only ever counts persisted work).
	WouldExamine int `json:"would_examine,omitempty"`
}

// Report is the whole scan's outcome.
type Report struct {
	Agents []*AgentReport `json:"agents"`
	DryRun bool           `json:"dry_run,omitempty"`
}

// Totals sums the per-agent counters.
func (r *Report) Totals() AgentReport {
	var t AgentReport
	for _, a := range r.Agents {
		t.Seen += a.Seen
		t.Unchanged += a.Unchanged
		t.Ingested += a.Ingested
		t.Skipped += a.Skipped
		t.Errors += a.Errors
		t.Lines += a.Lines
		t.Spans += a.Spans
		t.WouldExamine += a.WouldExamine
	}
	return t
}

// Scan walks every scannable session store, stat-diffs each file against the
// ledger, and feeds new/changed files through the gated ingest pipeline. It is
// incremental and idempotent: an unchanged file costs one map lookup; a grown
// append-log ingests only its tail; a rewritten document replaces its rows.
func Scan(ctx context.Context, s Store, opts Options) (*Report, error) {
	rep := &Report{DryRun: opts.DryRun}
	for _, st := range Scannable(opts.Agents) {
		ar, err := scanStore(ctx, s, st, opts)
		if err != nil {
			return rep, err
		}
		rep.Agents = append(rep.Agents, ar)
	}
	return rep, nil
}

// statSettled reports whether the ledger has already settled this file under a
// policy at least as broad as the current scan — i.e. the stat gate may skip
// re-examining it. An 'error' outcome is never settled: a transient ingest
// failure must retry next scan even when the file hasn't changed
// (document-layout files are rewritten atomically, so a failed one may never
// change again). A file the ledger only *skipped* (skill-less under the gate)
// is NOT settled once --keep-all widens the criteria — re-examine it so a
// session a prior plain discover skipped stays recoverable instead of being
// silenced by the stat match.
func statSettled(prev *store.ScannedFile, c candidate, opts Options) bool {
	if prev == nil || prev.Status == store.ScanStatusError {
		return false
	}
	if prev.Size != c.size || prev.MtimeMs != c.mtimeMs {
		return false
	}
	if opts.KeepAll && prev.Status == store.ScanStatusSkipped {
		return false
	}
	return true
}

// scanStore scans one agent's store.
func scanStore(ctx context.Context, s Store, st SessionStore, opts Options) (*AgentReport, error) {
	ar := &AgentReport{Agent: st.Agent}
	candidates, err := enumerate(st, opts.Since)
	if err != nil {
		return ar, err
	}
	if st.Layout == LayoutSQLite {
		return ar, scanSQLiteStore(ctx, s, st, candidates, opts, ar)
	}
	ar.Seen = len(candidates)
	if len(candidates) == 0 {
		return ar, nil
	}

	ledger, err := s.GetScannedFiles(ctx, st.Agent)
	if err != nil {
		return ar, err
	}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return ar, err
		}
		if statSettled(ledger[c.path], c, opts) {
			ar.Unchanged++
			continue
		}
		if opts.DryRun {
			ar.WouldExamine++
			continue
		}
		scanOneFile(ctx, s, st, c, opts, ar)
	}
	return ar, nil
}

// scanSQLiteStore handles database-backed stores (hermes state.db). The db
// file is stat-diffed like any candidate — but WAL mode means new messages
// can live in the -wal sibling while the main file's stat is unchanged, so
// the ledger entry folds the sibling's size/mtime in. Per-session
// incrementality lives in rawtrace's message-id cursors, so re-reading an
// unchanged session costs one cursor lookup.
func scanSQLiteStore(ctx context.Context, s Store, st SessionStore, candidates []candidate, opts Options, ar *AgentReport) error {
	ar.Seen = len(candidates)
	if len(candidates) == 0 {
		return nil
	}
	ledger, err := s.GetScannedFiles(ctx, st.Agent)
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		c = foldWALStat(c)
		if statSettled(ledger[c.path], c, opts) {
			ar.Unchanged++
			continue
		}
		if opts.DryRun {
			ar.WouldExamine++
			continue
		}
		examineSQLiteStore(ctx, s, st.Agent, c, opts, ar)
	}
	return nil
}

// examineSQLiteStore ingests one database candidate, folds the per-session
// results into the agent report, and records the file's ledger entry.
// entry.SessionID stays zero: a SQLite store holds MANY sessions, and
// recording just the last-ingested one would read as the file's only session
// to anything auditing the ledger.
func examineSQLiteStore(ctx context.Context, s Store, agent string, c candidate, opts Options, ar *AgentReport) {
	results, ierr := ingestSQLiteStore(ctx, s, agent, c.path, !opts.KeepAll)
	entry := &store.ScannedFile{
		AgentName:  agent,
		SourcePath: c.path,
		Size:       c.size,
		MtimeMs:    c.mtimeMs,
		Status:     store.ScanStatusIngested,
	}
	if ierr != nil {
		ar.Errors++
		entry.Status = store.ScanStatusError
	}
	ingested := 0
	for _, res := range results {
		if res.Skipped {
			ar.Skipped++
			continue
		}
		ingested++
		ar.Ingested++
		ar.Lines += res.LinesStored
		ar.Spans += res.SpansStored
	}
	// A database whose every session was gate-skipped is a skip, not an
	// ingest — same ledger semantics as the file-layout path.
	if ierr == nil && len(results) > 0 && ingested == 0 {
		entry.Status = store.ScanStatusSkipped
	}
	_ = s.UpsertScannedFile(ctx, entry)
}

// ingestSQLiteStore dispatches a database-backed store to its agent-specific
// reader. An agent whose descriptor says LayoutSQLite but has no reader here
// is a wiring bug surfaced as an error, never silently skipped.
func ingestSQLiteStore(ctx context.Context, s Store, agent, path string, gate bool) ([]*rawtrace.Result, error) {
	switch agent {
	case "hermes":
		return rawtrace.IngestHermesStateDB(ctx, s, path, gate)
	case "opencode":
		return rawtrace.IngestOpencodeDB(ctx, s, path, gate)
	default:
		return nil, fmt.Errorf("discover: no sqlite reader for agent %q", agent)
	}
}

// foldWALStat folds a SQLite database's -wal sibling into its stat signature,
// so a checkpoint-lagged write still flips the change detector.
func foldWALStat(c candidate) candidate {
	if fi, err := os.Stat(c.path + "-wal"); err == nil {
		c.size += fi.Size()
		if m := fi.ModTime().UnixMilli(); m > c.mtimeMs {
			c.mtimeMs = m
		}
	}
	return c
}

// cwdUnder reports whether the recorded working dir is at or under the scope
// dir. Both are canonicalized (symlinks resolved) before comparison so a macOS
// "/tmp/proj" scope matches a recorded "/private/tmp/proj" (and vice versa);
// matching is then exact or a path-segment prefix, so "/p/proj" scopes
// "/p/proj" and "/p/proj/sub" but not "/p/proj-other". An empty recorded cwd
// never matches — a scoped scan excludes sessions it can't place.
func cwdUnder(recorded, scope string) bool {
	if recorded == "" {
		return false
	}
	recorded = canonicalDir(recorded)
	scope = canonicalDir(scope)
	return recorded == scope || strings.HasPrefix(recorded, scope+string(filepath.Separator))
}

// canonicalDir resolves symlinks in a path so /tmp and /private/tmp compare
// equal on macOS. When the full path doesn't exist (a since-removed project, or
// a subdir below an existing root), it canonicalizes the deepest existing
// ancestor and re-appends the rest — so /tmp/gone still maps to
// /private/tmp/gone rather than staying uncanonicalized.
func canonicalDir(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p { // reached the root; nothing left to resolve
		return p
	}
	return filepath.Join(canonicalDir(parent), filepath.Base(p))
}

// scanOneFile ingests one new/changed file and records the outcome in the
// ledger. Ingest errors are per-file (counted, never fatal to the scan).
func scanOneFile(ctx context.Context, s Store, st SessionStore, c candidate, opts Options, ar *AgentReport) {
	// --cwd scope: skip files outside the requested project WITHOUT touching the
	// ledger, so an unscoped scan later still considers them. A file whose cwd
	// can't be determined is excluded too — a scoped scan wants only the project
	// it named, not unknowns.
	if opts.Cwd != "" && !cwdUnder(rawtrace.SniffWorkingDir(c.path), opts.Cwd) {
		return
	}
	res, err := rawtrace.Ingest(ctx, s, rawtrace.IngestParams{
		Agent:     st.Agent,
		Path:      c.path,
		SkillGate: !opts.KeepAll,
		Document:  st.Layout == LayoutDocument,
	})

	entry := &store.ScannedFile{
		AgentName:  st.Agent,
		SourcePath: c.path,
		Size:       c.size,
		MtimeMs:    c.mtimeMs,
	}
	switch {
	case err != nil:
		ar.Errors++
		entry.Status = store.ScanStatusError
	case res.Skipped:
		ar.Skipped++
		entry.Status = store.ScanStatusSkipped
	default:
		ar.Ingested++
		ar.Lines += res.LinesStored
		ar.Spans += res.SpansStored
		entry.Status = store.ScanStatusIngested
		entry.SessionID = res.SessionID
	}
	if entry.Status == store.ScanStatusError {
		entry.SessionID = uuid.Nil
	}
	// A ledger write failure is non-fatal: the worst case is re-examining the
	// file next scan, which the cursor/replace semantics make idempotent.
	_ = s.UpsertScannedFile(ctx, entry)
}
