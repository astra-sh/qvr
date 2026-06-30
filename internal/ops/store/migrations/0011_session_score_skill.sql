-- 0011: scope a grade to (skill, session), not just a session.
--
-- A grade asserts "this skill's run scored X", not "this session scored X". A
-- single session can load several skills; the prior session-grain key
-- (agent, session_ref, metric) let one grade double-count — the content rollup
-- joined it onto EVERY content cohort the session touched, inflating each
-- skill's mean by the others' grades. Adding skill to the key, and to the rollup
-- join (see store.contentScores), makes a grade contribute to exactly the skill
-- it judged.
--
--   skill   the graded skill's canonical name (skill.name), stored LOWERCASED to
--           match the value the rollup extracts from the span attributes. It is
--           in the PRIMARY KEY so one run can carry an independent grade per
--           skill it loaded (the agent x session x metric x skill matrix), and
--           INSERT OR REPLACE re-grades that one tuple in place.
--
-- SQLite cannot ALTER a PRIMARY KEY, so the table is rebuilt. Pre-0011 grades
-- carried no skill; they copy across with skill='' and, because the rollup join
-- now requires a non-empty skill match, drop out of the rollup — they are
-- exactly the ambiguous, double-counting grades this migration retires. Losing
-- their (un-attributable) attribution is acceptable for a moving dev/store, the
-- same disposition migration 0010 took for the dead eval tables.

ALTER TABLE session_score RENAME TO session_score_pre0011;

CREATE TABLE session_score (
  agent       TEXT    NOT NULL,
  session_ref TEXT    NOT NULL,
  skill       TEXT    NOT NULL DEFAULT '',
  metric      TEXT    NOT NULL DEFAULT 'score',
  value       REAL    NOT NULL,
  grader      TEXT    NOT NULL DEFAULT '',
  scored_at   INTEGER NOT NULL,
  PRIMARY KEY (agent, session_ref, skill, metric)
);

INSERT INTO session_score (agent, session_ref, skill, metric, value, grader, scored_at)
  SELECT agent, session_ref, '', metric, value, grader, scored_at
  FROM session_score_pre0011;

DROP TABLE session_score_pre0011;
