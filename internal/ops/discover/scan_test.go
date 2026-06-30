package discover_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/ops/discover"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// The fixtures mirror Claude Code's transcript shape: a session that loads a
// skill (a "Skill" tool_use) and one that doesn't.
const (
	scanUser       = `{"type":"user","sessionId":"%s","cwd":"/tmp/proj","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"do the thing"}}`
	scanPlain      = `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":3},"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/x"}},{"type":"text","text":"done"}]}}`
	scanWithSkill  = `{"type":"assistant","timestamp":"2026-06-02T00:00:02.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":3},"content":[{"type":"tool_use","id":"t2","name":"Skill","input":{"command":"code-review"}},{"type":"text","text":"ok"}]}}`
	skillSessionID = "550e8400-e29b-41d4-a716-446655440000"
	plainSessionID = "660e8400-e29b-41d4-a716-446655440000"
)

func openStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), store.OpenOptions{Path: filepath.Join(t.TempDir(), "ops.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// claudeStoreFixture writes a claude-projects-shaped tree with one
// skill-bearing and one skill-less session, returning the root and file paths.
func claudeStoreFixture(t *testing.T) (root, skillFile, plainFile string) {
	t.Helper()
	// Canonicalize so ledger keys (the walker resolves symlinked roots) match
	// the fixture paths on macOS, where tmp dirs sit behind /var → /private/var.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slug := filepath.Join(root, "-Users-x-proj")
	skillFile = filepath.Join(slug, skillSessionID+".jsonl")
	plainFile = filepath.Join(slug, plainSessionID+".jsonl")
	writeLines(t, skillFile, sprintfUser(skillSessionID), scanWithSkill)
	writeLines(t, plainFile, sprintfUser(plainSessionID), scanPlain)
	return root, skillFile, plainFile
}

func sprintfUser(sid string) string {
	out := make([]byte, 0, len(scanUser)+len(sid))
	for i := 0; i < len(scanUser); i++ {
		if scanUser[i] == '%' && i+1 < len(scanUser) && scanUser[i+1] == 's' {
			out = append(out, sid...)
			i++
			continue
		}
		out = append(out, scanUser[i])
	}
	return string(out)
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scanClaude runs a scan restricted to the claude store rooted at root.
func scanClaude(t *testing.T, s store.Store, root string, opts discover.Options) *discover.Report {
	t.Helper()
	// Point the claude store at the fixture tree via HOME: the store root is
	// ~/.claude/projects, so a fake HOME hosts the fixture.
	home := filepath.Dir(filepath.Dir(root))
	_ = home
	t.Setenv("HOME", fakeHomeFor(t, root))
	opts.Agents = []string{"claude"}
	rep, err := discover.Scan(context.Background(), s, opts)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return rep
}

// fakeHomeFor builds a HOME dir whose ~/.claude/projects is a symlink to the
// fixture tree.
func fakeHomeFor(t *testing.T, projectsRoot string) string {
	t.Helper()
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectsRoot, filepath.Join(claudeDir, "projects")); err != nil {
		t.Fatal(err)
	}
	return home
}

func agentReport(t *testing.T, rep *discover.Report, agent string) *discover.AgentReport {
	t.Helper()
	for _, a := range rep.Agents {
		if a.Agent == agent {
			return a
		}
	}
	t.Fatalf("no report for %s: %+v", agent, rep.Agents)
	return nil
}

// TestScan_GateIngestsSkillSkipsSkillless is the core scan contract: the
// skill-bearing session lands (rows + meta + ledger 'ingested'); the
// skill-less one is skipped pre-persist (no rows, ledger 'skipped_no_skill').
func TestScan_GateIngestsSkillSkipsSkillless(t *testing.T) {
	s := openStore(t)
	root, skillFile, plainFile := claudeStoreFixture(t)

	rep := scanClaude(t, s, root, discover.Options{})
	ar := agentReport(t, rep, "claude")
	if ar.Seen != 2 || ar.Ingested != 1 || ar.Skipped != 1 || ar.Errors != 0 {
		t.Fatalf("report = %+v, want seen 2 / ingested 1 / skipped 1", ar)
	}

	// The skill session is in the unified model; the plain one stored nothing.
	metas, err := s.ListSessionMeta(context.Background(), nil)
	if err != nil {
		t.Fatalf("list meta: %v", err)
	}
	if len(metas) != 1 || metas[0].SessionID != uuid.MustParse(skillSessionID) {
		t.Fatalf("session meta = %+v, want only the skill session", metas)
	}
	if metas[0].WorkingDir != "/tmp/proj" || len(metas[0].Skills) != 1 {
		t.Errorf("meta = %+v, want cwd-scoped with one skill", metas[0])
	}

	// Ledger reflects both outcomes, keyed by file.
	ledger, err := s.GetScannedFiles(context.Background(), "claude")
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if got := ledger[skillFile]; got == nil || got.Status != store.ScanStatusIngested {
		t.Errorf("skill file ledger = %+v, want ingested", got)
	}
	if got := ledger[plainFile]; got == nil || got.Status != store.ScanStatusSkipped {
		t.Errorf("plain file ledger = %+v, want skipped_no_skill", got)
	}
}

// TestScan_SecondPassIsStatNoOp pins incrementality: an unchanged store costs
// only ledger lookups — nothing re-derives, nothing is stored twice.
func TestScan_SecondPassIsStatNoOp(t *testing.T) {
	s := openStore(t)
	root, _, _ := claudeStoreFixture(t)

	scanClaude(t, s, root, discover.Options{})
	rep := scanClaude(t, s, root, discover.Options{})
	ar := agentReport(t, rep, "claude")
	if ar.Unchanged != 2 || ar.Ingested != 0 || ar.Skipped != 0 {
		t.Errorf("second pass = %+v, want all unchanged", ar)
	}
}

// TestScan_SkippedFileGrowsAndGainsSkill pins self-healing: a previously
// skipped session that resumes and uses a skill is re-derived in full (cursor
// never advanced) and persisted.
func TestScan_SkippedFileGrowsAndGainsSkill(t *testing.T) {
	s := openStore(t)
	root, _, plainFile := claudeStoreFixture(t)
	scanClaude(t, s, root, discover.Options{})

	f, err := os.OpenFile(plainFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(scanWithSkill + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	rep := scanClaude(t, s, root, discover.Options{})
	ar := agentReport(t, rep, "claude")
	if ar.Ingested != 1 {
		t.Fatalf("grown file: report %+v, want 1 ingested", ar)
	}
	sid := uuid.MustParse(plainSessionID)
	meta, err := s.GetSessionMeta(context.Background(), sid)
	if err != nil || meta == nil {
		t.Fatalf("grown session missing from unified model: %v %v", meta, err)
	}
	// Full file persisted (3 lines), not just the tail.
	rows, _ := s.QueryRawTraces(context.Background(), &store.RawTraceFilter{SessionID: &sid})
	if len(rows) != 3 {
		t.Errorf("rows = %d, want full 3-line re-derive from offset 0", len(rows))
	}
}

// TestScan_PruneThenRescanDoesNotReingest is the churn-loop regression: after
// a kept session is deleted (gc/retention), an unchanged file must NOT be
// re-ingested — the file-keyed ledger survives DeleteSession.
func TestScan_PruneThenRescanDoesNotReingest(t *testing.T) {
	s := openStore(t)
	root, _, _ := claudeStoreFixture(t)
	scanClaude(t, s, root, discover.Options{})

	if _, err := s.DeleteSession(context.Background(), uuid.MustParse(skillSessionID)); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	rep := scanClaude(t, s, root, discover.Options{})
	ar := agentReport(t, rep, "claude")
	if ar.Ingested != 0 || ar.Unchanged != 2 {
		t.Errorf("post-prune rescan = %+v, want all unchanged (no churn loop)", ar)
	}
}

// TestScan_KeepAllIngestsEverything pins --keep-all: the gate is off, the
// skill-less session is stored too.
func TestScan_KeepAllIngestsEverything(t *testing.T) {
	s := openStore(t)
	root, _, _ := claudeStoreFixture(t)

	rep := scanClaude(t, s, root, discover.Options{KeepAll: true})
	ar := agentReport(t, rep, "claude")
	if ar.Ingested != 2 || ar.Skipped != 0 {
		t.Errorf("keep-all = %+v, want 2 ingested", ar)
	}
}

// TestScan_KeepAllReingestsPreviouslySkipped pins #267: a session a prior plain
// discover only *skipped* (skill-less under the gate) must still be recoverable
// by a later --keep-all run, even though the file is stat-unchanged. The
// stat-ledger short-circuit must not silence the broader policy.
func TestScan_KeepAllReingestsPreviouslySkipped(t *testing.T) {
	s := openStore(t)
	root, _, plainFile := claudeStoreFixture(t)

	// First pass without --keep-all: the plain session is skipped and the file
	// is recorded as skipped_no_skill in the ledger.
	rep := scanClaude(t, s, root, discover.Options{})
	if ar := agentReport(t, rep, "claude"); ar.Ingested != 1 || ar.Skipped != 1 {
		t.Fatalf("first pass = %+v, want ingested 1 / skipped 1", ar)
	}

	// Second pass WITH --keep-all on the unchanged tree: the previously-skipped
	// file must be re-examined and ingested; the already-ingested skill file
	// stays unchanged.
	rep = scanClaude(t, s, root, discover.Options{KeepAll: true})
	ar := agentReport(t, rep, "claude")
	if ar.Ingested != 1 || ar.Unchanged != 1 || ar.Skipped != 0 {
		t.Fatalf("keep-all rescan = %+v, want ingested 1 / unchanged 1 / skipped 0", ar)
	}

	// The previously-skipped session is now in the unified model, and its ledger
	// row flipped from skipped to ingested.
	sid := uuid.MustParse(plainSessionID)
	if meta, err := s.GetSessionMeta(context.Background(), sid); err != nil || meta == nil {
		t.Fatalf("recovered session missing from unified model: %v %v", meta, err)
	}
	ledger, err := s.GetScannedFiles(context.Background(), "claude")
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if got := ledger[plainFile]; got == nil || got.Status != store.ScanStatusIngested {
		t.Errorf("plain file ledger = %+v, want ingested after keep-all rescan", got)
	}
}

// TestScan_ErrorOutcomeRetriesUnchangedFile pins the no-error-caching rule: a
// ledger row recorded as 'error' must NOT satisfy the stat gate — the file is
// re-examined next scan even though its size/mtime are unchanged (document
// files are rewritten atomically, so a failed one may never change again).
func TestScan_ErrorOutcomeRetriesUnchangedFile(t *testing.T) {
	s := openStore(t)
	root, skillFile, _ := claudeStoreFixture(t)

	// Simulate a prior scan that failed on the skill file: ledger holds an
	// error row with the file's CURRENT stats.
	info, err := os.Stat(skillFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertScannedFile(context.Background(), &store.ScannedFile{
		AgentName: "claude", SourcePath: skillFile,
		Size: info.Size(), MtimeMs: info.ModTime().UnixMilli(),
		Status: store.ScanStatusError,
	}); err != nil {
		t.Fatal(err)
	}

	rep := scanClaude(t, s, root, discover.Options{})
	ar := agentReport(t, rep, "claude")
	if ar.Ingested != 1 {
		t.Errorf("errored file must retry on rescan: report %+v, want 1 ingested", ar)
	}
}

// TestScan_DryRunPersistsNothing pins --dry-run: it reports work to do but
// writes neither rows nor ledger entries.
func TestScan_DryRunPersistsNothing(t *testing.T) {
	s := openStore(t)
	root, _, _ := claudeStoreFixture(t)

	rep := scanClaude(t, s, root, discover.Options{DryRun: true})
	ar := agentReport(t, rep, "claude")
	if !rep.DryRun || ar.WouldExamine != 2 || ar.Ingested != 0 {
		t.Errorf("dry-run report = %+v, want would_examine=2 ingested=0", ar)
	}
	metas, _ := s.ListSessionMeta(context.Background(), nil)
	ledger, _ := s.GetScannedFiles(context.Background(), "claude")
	if len(metas) != 0 || len(ledger) != 0 {
		t.Errorf("dry-run persisted: %d metas, %d ledger rows; want 0/0", len(metas), len(ledger))
	}
}

// TestScan_CwdScopeExcludesOtherProjects pins --cwd: a scoped scan records only
// sessions whose working directory is at/under the scope, and a stray session
// from another project is excluded WITHOUT a ledger entry (so a later unscoped
// scan still considers it).
func TestScan_CwdScopeExcludesOtherProjects(t *testing.T) {
	s := openStore(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slug := filepath.Join(root, "-Users-x-proj")
	const aID = "550e8400-e29b-41d4-a716-446655440000"
	const bID = "660e8400-e29b-41d4-a716-446655440000"
	userLine := func(sid, cwd string) string {
		return `{"type":"user","sessionId":"` + sid + `","cwd":"` + cwd +
			`","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"x"}}`
	}
	writeLines(t, filepath.Join(slug, aID+".jsonl"), userLine(aID, "/work/projA"), scanWithSkill)
	writeLines(t, filepath.Join(slug, bID+".jsonl"), userLine(bID, "/work/projB"), scanWithSkill)

	rep := scanClaude(t, s, root, discover.Options{Cwd: "/work/projA"})
	ar := agentReport(t, rep, "claude")
	if ar.Ingested != 1 {
		t.Fatalf("cwd scope must ingest only the in-scope session, ingested=%d", ar.Ingested)
	}
	if ar.Skipped != 0 {
		t.Errorf("out-of-scope file must be excluded without a ledger entry, skipped=%d", ar.Skipped)
	}

	// Unscoped rescan still picks up the previously-excluded project (it was
	// never ledgered).
	rep2 := scanClaude(t, s, root, discover.Options{})
	if ar2 := agentReport(t, rep2, "claude"); ar2.Ingested != 1 {
		t.Errorf("unscoped rescan must now ingest the previously out-of-scope session, ingested=%d", ar2.Ingested)
	}
}
