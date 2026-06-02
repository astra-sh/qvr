-- 0002_raw_traces.sql
-- Raw, lossless trace capture. Stores agent-native transcript lines and hook
-- payloads VERBATIM, with zero normalization — no action_type mapping, no typed
-- payloads, no truncation, no $schema envelope. This is the canonical source of
-- truth; any normalized/derived view (spans, skill attribution, dashboards) is
-- computed downstream from these bytes and can be regenerated at will.
--
-- Idempotent via IF NOT EXISTS, matching 0001.

CREATE TABLE IF NOT EXISTS raw_traces (
  id               TEXT PRIMARY KEY,            -- uuid (capture-assigned)
  agent_name       TEXT NOT NULL,               -- claude-code | codex | cursor | ...
  session_id       TEXT NOT NULL,               -- canonical uuid (correlation key)
  agent_session_id TEXT,                         -- raw session id string from the agent
  source           TEXT NOT NULL,               -- 'transcript' | 'hook_payload'
  source_path      TEXT,                         -- transcript file (NULL for hook_payload)
  working_directory TEXT,                        -- cwd reported by the hook (project scoping)
  hook_type        TEXT,                         -- hook event name (hook_payload rows only)
  byte_offset      INTEGER NOT NULL DEFAULT 0,  -- start offset of this line in source_path
  seq              INTEGER NOT NULL,            -- monotonic per session, capture order
  captured_at      DATETIME NOT NULL,           -- when qvr ingested it
  raw              BLOB NOT NULL                -- the verbatim native bytes, untouched
);

CREATE INDEX IF NOT EXISTS idx_raw_session_seq ON raw_traces(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_raw_agent_ts    ON raw_traces(agent_name, captured_at DESC);
CREATE INDEX IF NOT EXISTS idx_raw_source_path ON raw_traces(source_path);
CREATE INDEX IF NOT EXISTS idx_raw_wd          ON raw_traces(working_directory);

-- Per-source tailing cursor: the byte offset we last consumed in each transcript
-- file, so every hook firing resumes from there and never re-stores the same
-- bytes. The SQLite tx around (insert rows + bump cursor) makes capture atomic,
-- replacing the YAML-file + filelock state the reference implementation used.
CREATE TABLE IF NOT EXISTS trace_cursors (
  agent_name   TEXT NOT NULL,
  source_path  TEXT NOT NULL,
  byte_offset  INTEGER NOT NULL DEFAULT 0,
  session_id   TEXT,
  updated_at   DATETIME NOT NULL,
  PRIMARY KEY (agent_name, source_path)
);

-- Derived spans, persisted alongside raw. Spans are a PROJECTION of raw_traces
-- (OpenInference Turn/Tool/Skill spans); storing them lets us (a) confirm
-- raw↔span parity, and (b) evolve the deriver over time while keeping prior
-- derivations comparable. span_id is deterministic, so re-deriving a session
-- and re-storing is idempotent. deriver_version stamps which deriver produced
-- the row, so an improved deriver's output can be told apart from older runs.
CREATE TABLE IF NOT EXISTS spans (
  span_id          TEXT PRIMARY KEY,
  trace_id         TEXT NOT NULL,
  parent_span_id   TEXT,
  session_id       TEXT NOT NULL,
  agent_name       TEXT NOT NULL,
  kind             TEXT NOT NULL,
  name             TEXT,
  start_ms         INTEGER NOT NULL DEFAULT 0,
  end_ms           INTEGER NOT NULL DEFAULT 0,
  attributes       TEXT,                       -- JSON (OpenInference attributes)
  deriver_version  INTEGER NOT NULL DEFAULT 1,
  derived_at       DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_spans_session ON spans(session_id, start_ms);
CREATE INDEX IF NOT EXISTS idx_spans_trace   ON spans(trace_id);
CREATE INDEX IF NOT EXISTS idx_spans_kind    ON spans(kind);
