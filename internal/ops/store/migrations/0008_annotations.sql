-- 0008: human annotations on captured sessions — the outer-loop feedback
-- signal (a reviewer's verdict on what a skill actually did, with a reason).
-- Unlike spans and session_meta, an annotation is NOT a derived projection:
-- `qvr audit rederive` regenerates those from raw, which would erase a human
-- edit written there. The verdict therefore lives in its own table that
-- derivation never rewrites. A NULL skill is a whole-session annotation; a set
-- skill scopes the verdict to one skill within the session. Append-only: a
-- session can accrue several verdicts over time (the latest by created_at wins
-- for consumers that want one).
CREATE TABLE IF NOT EXISTS annotations (
  session_id  TEXT NOT NULL,
  skill       TEXT,
  outcome     TEXT NOT NULL,
  note        TEXT,
  author      TEXT,
  created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_annotations_session ON annotations(session_id);
CREATE INDEX IF NOT EXISTS idx_annotations_skill ON annotations(skill);
-- created_at backs the --since range filter on `audit annotations` / `ops lineage`.
CREATE INDEX IF NOT EXISTS idx_annotations_created ON annotations(created_at);
