-- 0008: indexes for the evolution loop's grain over SKILL spans — the version
-- cohort (skill.content_hash) and run-status (qvr.outcome) cuts that
-- SkillContentRollup and `qvr audit logs --version/--status` filter on.
--
-- Same contract as 0003: only write-mode opens apply migrations, so the queries
-- never DEPEND on these — they just cheapen the GROUP BYs / filters once a
-- write-mode consumer (capture / rederive / ingest) has run since this landed.

-- SKILL spans bucketed by the observed content hash (the comparison coordinate).
CREATE INDEX IF NOT EXISTS idx_spans_skill_content_hash
  ON spans(json_extract(attributes, '$."skill.content_hash"'))
  WHERE kind = 'SKILL';

-- SKILL spans filtered by run status (success / failure / blocked).
CREATE INDEX IF NOT EXISTS idx_spans_skill_outcome
  ON spans(json_extract(attributes, '$."qvr.outcome"'))
  WHERE kind = 'SKILL';
