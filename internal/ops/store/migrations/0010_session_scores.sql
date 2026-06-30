-- 0010: BYO-grader quality scores + dead-schema cleanup.
--
-- This is version 10, NOT 9, on purpose. The migrator keys by integer version
-- (schema_migrations.version is a set, see migrations/embed.go); an abandoned
-- {skill,commit}-grain eval substrate already recorded version 9 as applied on
-- seasoned developer DBs, so a 0009 file would be silently skipped there — the
-- one place it would actually be tested. Numbering past the burned slot makes
-- the migration run on fresh AND seasoned DBs alike.

-- Quality scores: a BYO grader's verdict attached to a run by the agent-native
-- session id. A grade is NOT derivable from the transcript (it needs an expected
-- value and a judge), so unlike qvr.outcome it cannot live on a derived span — a
-- re-derive would wipe it. It lives here, in a side table the derive pipeline
-- never reads or writes (the same survives-rederive treatment session_lock_snapshot
-- gets in 0005). The grader runs outside qvr; qvr stores the verdict + provenance
-- and the content-cohort rollup joins it through session_meta.source_session_id.
--
--   agent       canonical agent name, matching session_meta.agent_name — in the
--               PK so a cross-agent id collision can't land a grade on the wrong
--               run (the {variant}x{agent} matrix is the goal).
--   session_ref the agent-native session id, stored LOWERCASED. claude stores it
--               verbatim (uppercase) in session_meta.source_session_id while the
--               canonical session_id is lowercased; normalizing both write and
--               join keeps a grader that echoes a lowercase id from --output-format
--               json from silently orphaning.
--   value       1=pass, 0=fail, or any numeric grade; mean over a cohort = pass-rate.
--   grader      "exact"/"regex"/"llm:<model>" — provenance only. The I/O is already
--               in the trace (gen_ai.* + the tool args), so it is NOT duplicated here.
CREATE TABLE IF NOT EXISTS session_score (
  agent       TEXT    NOT NULL,
  session_ref TEXT    NOT NULL,
  metric      TEXT    NOT NULL DEFAULT 'score',
  value       REAL    NOT NULL,
  grader      TEXT    NOT NULL DEFAULT '',
  scored_at   INTEGER NOT NULL,
  PRIMARY KEY (agent, session_ref, metric)
);

-- Bury the dead. These tables have no reader in the shipped code; this makes the
-- schema match it. The first three are the removed eval substrate (3111272,
-- stripped before 0.30); the last four are the pre-raw-trace normalized-event
-- schema from 0001 that 0002+ superseded. Dropping them is a one-way cleanup on
-- existing DBs — acceptable for a moving dev/store, and the data is already empty.
DROP TABLE IF EXISTS annotations;
DROP TABLE IF EXISTS eval_runs;
DROP TABLE IF EXISTS eval_case_results;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS skill_versions;
DROP TABLE IF EXISTS self_audits;
