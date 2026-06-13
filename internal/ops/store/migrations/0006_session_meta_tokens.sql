-- 0006: nullable session token totals on the unified session read model.
-- Filled at derive time from the spans' gen_ai.usage.* attributes (or
-- natively, for stores that report usage only at session level). NULL means
-- the agent's native store reported no usage for that side — consumers render
-- n/a, never 0, so token-less agents can't poison cross-agent comparisons.
ALTER TABLE session_meta ADD COLUMN tokens_in INTEGER;
ALTER TABLE session_meta ADD COLUMN tokens_out INTEGER;
