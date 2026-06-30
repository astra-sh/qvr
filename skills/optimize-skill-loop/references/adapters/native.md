# Native eval-source adapter

The default eval source: a skill instrumented by `create-skill-eval`, carrying a
native `eval/` directory (`HARNESS.md`, `scenarios.jsonl`, `eval.py`,
`rubric.yaml`, fixtures). This adapter has **no external dependency** — it is the
`run-cohort.sh` + `eval.py` + `qvr audit` pipeline, assembled into the normalized
cohort the loop consumes (see `../eval-source-boundary.md`).

## `run(skill, version, agents, n, models, eval_dir)`

1. **Cohort** — fresh **naked** headless sessions, frozen scenarios, only `input`
   sent. The runner mints no session id and no run-key:
   ```
   EVAL=.claude/skills/<skill>/eval                 # the frozen grader, current version
   bash scripts/run-cohort.sh --skill <skill> --version <version> \
     --scenarios "$EVAL/scenarios.jsonl" --agents <agents> --n <n> \
     --out run/runs/<version>.jsonl
   ```
2. **Capture** — let qvr ingest the sessions for every agent (no correlation step):
   ```
   qvr audit discover --agent <agents>     # qvr owns session ids, agent, cost, subtree_hash
   ```
3. **Deterministic metrics** — `grade-cohort.sh` reads the metric ids from
   `HARNESS.md`, runs `eval.py` per metric (pure `output` vs `expected`, no session
   ids), and **version-tags every score row**:
   ```
   bash scripts/grade-cohort.sh --skill <skill> --version <version> \
     --runs run/runs/<version>.jsonl --eval "$EVAL" --out-dir run/scores
   ```
   The `version` stamp is the join key: `report.py` aggregates per (agent, version)
   from the score rows — never through a session id.
4. **Rubric** — you are the judge; qvr supplies the session ids (filter
   `qvr audit sessions` to this project + agent + the cohort's `subtree_hash` in
   `skill_versions[]`), then judge each transcript against `eval/rubric.yaml` and
   optionally annotate:
   ```
   qvr audit export --session <id> --source transcript
   qvr audit annotate <id> --skill <skill> --metric rubric --score <0..1> --grader llm-judge --agent <agent>
   ```
5. **Cost** — from the ledger, attributed by skill identity by `report.py`:
   ```
   qvr audit sessions --since 1h --limit 0 --output json   # tokens_out / turns / tools + agent + skill_versions[]
   ```

The normalized cohort is the join of `run/runs/*.jsonl` (run identity + version),
the `run/scores/*.jsonl` metric rows, and the `sessions` cost view (attributed by
`subtree_hash`) — exactly the inputs `report.py` consumes, so the native path feeds
the report directly without a conversion step.

## Conformance

Before the first cohort, validate the source:
```
python3 .claude/skills/create-skill-eval/scripts/validate-harness.py \
  --skill <skill> --eval .claude/skills/<skill>/eval
```
A non-conformant `eval/` stops the loop before any run.
