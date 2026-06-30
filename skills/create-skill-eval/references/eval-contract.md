# The `eval/` contract — the seam between `create-skill-eval` and `optimize-skill-loop`

This is the **interface** the two skills agree on. `create-skill-eval` *produces*
a conformant `eval/` directory **inside an inner skill**; `optimize-skill-loop`
*validates then consumes* it read-only and never edits it. As long as a skill's
`eval/` satisfies this contract, the optimizer can run the loop over it without
knowing anything about what the skill does.

> One sentence: **the eval is the inner skill's own asset; the optimizer is pure
> mechanism.** Grading text2sql (run the SQL, diff rows) vs slugify (string match)
> vs a codemod (build + test) differs entirely — that difference lives here, in
> the skill's `eval/`, never in the optimizer.

## Directory layout (ships inside the inner skill, frozen)

```
<skill>/eval/
  HARNESS.md            # REQUIRED — the manifest the optimizer reads (see below)
  scenarios.jsonl       # REQUIRED — frozen cases, one JSON object per line
  eval.py               # REQUIRED — deterministic grader CLI (see CLI contract)
  rubric.yaml           # REQUIRED — frozen LLM-judge dimensions/weights/anchors
  <fixtures + builders>  # OPTIONAL — whatever eval.py needs (e.g. fixture.db, build-*.py)
```

The whole dir is **frozen** once the loop starts: `git diff <baseline> <candidate>
-- '*/eval/*'` must be empty for every candidate. Only the skill's *behaviour*
files (SKILL.md, its scripts/references) may change between rounds.

## `HARNESS.md` — the manifest (the part that makes the optimizer skill-agnostic)

A short front-matter block the optimizer parses to know how to run cohorts, which
metrics exist, and how to build the frontier — **without** hardcoding anything
skill-specific. Required keys:

```yaml
---
skill: skillops-sql            # the inner skill this harness grades
metrics:                       # every deterministic metric eval.py emits
  - id: exact                  # canonical correctness metric (REQUIRED, ∈[0,1])
    axis: quality              # quality | cost | perf  — which frontier axis it feeds
    direction: max             # max | min
    optimizer_role: gate       # gate (drives the exit predicate) | frontier | tiebreak
  - id: perf
    axis: perf
    direction: max
    optimizer_role: frontier
rubric: rubric.yaml            # the LLM-judge dimensions file
cost:                          # the cost axis comes from the ledger, not eval.py
  source: ledger               # qvr audit sessions -> tokens_out
  axis: cost
  direction: min
agents: [claude, codex]        # frozen fan-out set
n: 2                           # runs per agent per variant
models: {claude: claude-sonnet-4-6, codex: gpt-5.5}   # pinned for comparable cost
exit: {target_quality: 1.0, max_rounds: 6, patience: 2}
prompt_template: prompt.txt    # OPTIONAL override of run-cohort.sh's default template
fixtures: [fixture.db, fixture-perf.db]   # files eval.py needs present
---

# free-form prose below: what "correct" means for THIS skill, the scenario
# rationale, the freeze contract, anything a human reviewer should read.
```

The `metrics[].axis` + `direction` + `optimizer_role` triple is what lets the
optimizer assemble an N-axis pareto frontier generically: every `frontier`/`gate`
metric becomes an axis; `gate` is what `--target-quality` is checked against.

## `scenarios.jsonl` — the frozen cases

One JSON object per line:

```jsonl
{"id": "s1", "input": "<sent to the agent verbatim>", "expected": <golden, ANY json>}
{"id": "s5_open", "input": "<sent to the agent>"}        # no `expected` ⇒ rubric-only
```

- `input` is the **only** field ever sent to the agent. `expected` is used
  out-of-band by `eval.py` and must never reach the prompt.
- `expected` may be any JSON the grader understands — a string (string-match), a
  result set (`[[8]]`, text2sql), a schema, an exit code. Its shape is the
  skill's business; the optimizer never looks inside it.
- A row **without** `expected` is input-only: it skips every deterministic metric
  and is judged by the rubric alone. (`quality` = pass rate over the `expected`
  rows only.)

## `eval.py` — the deterministic grader CLI

The grader is **pure**: no LLM, no network, no clock, no randomness — same inputs
⇒ byte-identical scores, so every cohort is judged identically. Required CLI:

```
python3 eval.py --runs <runs.jsonl> --scenarios <scenarios.jsonl> \
                --skill <name> --metric <metric-id> --out <scores.jsonl>
python3 eval.py --runs ... --scenarios ... --metric <id> --explain   # human table, no --out
```

- `--metric <id>` selects which manifest metric to emit (e.g. `exact`, `perf`).
  Supporting multiple metrics = handle each `--metric` value; emit one score row
  per gradable run.
- Output: JSONL in the exact shape `qvr audit annotate --from` consumes:
  ```json
  {"session": "<id>", "agent": "<a>", "skill": "<s>", "metric": "<id>", "score": <0..1>, "grader": "<id>"}
  ```
- **Version stamping (required for aggregation).** `eval.py` itself need not know
  the version, but the score rows the loop persists MUST carry a `"version"`
  field, because the report aggregates per (agent, version) **from the score
  rows** — a session-keyed join breaks when an agent reuses/collapses its native
  session id across runs. The native adapter does this in `grade-cohort.sh`
  (stamping `version` after `eval.py` emits each row); any other adapter must
  likewise tag its normalized rows with the version it ran.
- Every metric is normalized to **∈ [0,1]** (the optimizer compares axes
  uniformly; a raw latency in ms is a *companion* number, not the score — see the
  perf pattern in `behaviour-graders.md`).
- A run that errors (bad output, query fails to execute) scores `0.0`, never
  crashes the grader.
- Input-only scenarios (no `expected`) emit **no** row for deterministic metrics.

## `rubric.yaml` — the frozen LLM-judge rubric

Dimensions/weights/anchors on a `[0,1]` scale, aggregated by weighted sum. The
optimizer agent is the judge; it scores each captured session strictly against
these anchors and annotates one `metric: rubric` number. Frozen for the loop.

## What the optimizer guarantees in return

- It **reads** `HARNESS.md` to drive `run-cohort.sh` (agents, N, models, prompt),
  calls `eval.py` once per declared deterministic metric, applies `rubric.yaml`,
  pulls the cost axis from the ledger, and builds the frontier over the declared
  axes.
- It **never writes** inside `eval/`. If it needs `eval/` to change, that's a bug
  in the harness → fix it with `create-skill-eval` and re-freeze (new baseline).
- It validates conformance first (`scripts/validate-harness.py`): required
  files present, `HARNESS.md` parses, `eval.py` answers `--metric <each>`,
  fixtures present. A non-conformant `eval/` stops the loop before any cohort.

## Versioning note

The contract may grow (new optional `HARNESS.md` keys). Treat unknown keys as
forward-compatible: an older optimizer ignores keys it doesn't know; a newer one
defaults missing keys (e.g. a harness with only an `exact` metric and no `perf`
runs as the original 2-axis quality/cost loop).
