-- 0009: eval results, keyed by {skill_name, skill_commit} — the lock × evidence
-- join that is qvr's moat. An eval run grades a captured session against the
-- skill's evals.yaml; the verdict is pinned to the exact locked commit that
-- ran, so lineage can show "at commit abc the safety suite failed; at def it
-- passed". Per-case rows carry the granular pass/fail + reason.
CREATE TABLE IF NOT EXISTS eval_runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  skill_name    TEXT NOT NULL,
  skill_commit  TEXT NOT NULL,
  suite         TEXT NOT NULL,         -- requested suite, or '*' for all
  session_id    TEXT,                  -- the graded session
  started_at    DATETIME NOT NULL,
  passed        INTEGER NOT NULL,
  failed        INTEGER NOT NULL,
  pass          INTEGER NOT NULL       -- 1 only when every case passed
);
CREATE INDEX IF NOT EXISTS idx_eval_runs_skill ON eval_runs(skill_name, skill_commit);
CREATE INDEX IF NOT EXISTS idx_eval_runs_started ON eval_runs(started_at);

CREATE TABLE IF NOT EXISTS eval_case_results (
  eval_run_id   INTEGER NOT NULL,
  suite         TEXT NOT NULL,
  case_name     TEXT NOT NULL,
  pass          INTEGER NOT NULL,
  detail        TEXT
);
CREATE INDEX IF NOT EXISTS idx_eval_case_run ON eval_case_results(eval_run_id);
