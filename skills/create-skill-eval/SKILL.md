---
name: create-skill-eval
description: >-
  Author a frozen, graded eval harness for ONE inner skill and freeze it inside
  that skill as a conformant eval/ directory (scenarios + deterministic evaluator
  + rubric + fixtures + HARNESS.md manifest). The eval content is skill-specific;
  this skill is the reusable methodology that produces it. Its output is consumed
  read-only by optimize-skill-loop. Use BEFORE optimizing — when a skill needs a
  graded before/after harness built. Trigger phrases: "build an eval harness for
  <skill>", "create a skill eval", "instrument <skill> with a frozen grader",
  "write scenarios and a rubric for <skill>".
allowed-tools:
  - shell        # build fixtures, run validate-harness.py, qvr edit/publish
  - exec         # subprocess: eval.py dry-runs, fixture builders
  - file_read
  - file_write
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# create-skill-eval

Build the **graded harness** for one inner skill and freeze it into that skill's
`eval/` directory. This is step "instrument the baseline" factored out of the
optimizer: the *how to grade THIS skill* lives here and ships with the skill; the
*how to run the loop* lives in `optimize-skill-loop` and is skill-agnostic.

- **You produce** a conformant `eval/` dir (see `references/eval-contract.md`).
- **`optimize-skill-loop` consumes** it read-only and never edits it.

The `eval/` you freeze *is* the two halves of the loop's contract made concrete:
it **rides the skill's qvr version** (so the loop can `switch` to a content version
before each cohort), and its `eval.py` + `rubric.yaml` are what turn each captured
**qvr session → metrics ∈[0,1]**. qvr is the loop's inner ledger — it owns the
sessions, the cost, and the per-version identity; your harness only supplies the
grading. You never plumb session ids or cost here; you author *what correct means*
for this skill and hand it off frozen.

> The eval is **the inner skill's own asset.** Grading text2sql (run the SQL,
> diff the rows), slugify (string match), and a codemod (build + test) have
> nothing in common — that difference belongs in the skill's `eval/`, authored
> here, not baked into the generic optimizer.

## The deliverable: a conformant `eval/` (the contract)

Everything you write must satisfy `references/eval-contract.md` — that's the seam
the optimizer relies on. The frozen dir:

```
<skill>/eval/
  HARNESS.md          # manifest: metrics, axes, agents/N/models, exit, fixtures
  scenarios.jsonl     # frozen cases {id, input, expected?}
  eval.py             # deterministic grader CLI (--metric <id>, --explain)
  rubric.yaml         # frozen LLM-judge dimensions/weights/anchors
  <fixtures/builders> # whatever eval.py needs
```

Once frozen, the whole dir is byte-stable for the loop: `git diff <baseline>
<candidate> -- '*/eval/*'` is empty forever after.

## 0. Prereq

Requires **`python3`** on PATH — the grader you author (`eval.py`), its fixture
builders, and `scripts/validate-harness.py` all use it (stdlib only, no `pip
install`). PyYAML is optional: the validator falls back to a minimal manifest
parser when it's absent.

## 1. Pick (or scaffold) the inner skill

The harness instruments one inner skill. Either it already exists (`qvr list`
shows it), or scaffold a deliberately-minimal baseline to optimize up from:

```
qvr create <name> --type simple        # edit-mode lock entry under .claude/skills/<name>
# …write the minimal v0.1.0 behaviour into SKILL.md — the floor you optimize from…
```

A weak baseline is a feature: it gives the optimizer a long, legible learning
curve. Leave the skill in edit mode; step 5 freezes the harness into it.

## 2. Interview the user — design the harness for THIS skill

**Do not drop the templates in blind.** The grader has to fit this skill. Ask,
and lock in (these become the five frozen invariants):

- **What "correct" means — the deterministic check.** Walk the user through it:
  exact string? a value matched by regex? JSON satisfying a schema? a file that
  must build / pass a test? **a query whose result set must match?** Their answer
  decides what `eval.py`'s grade becomes. **Strongly prefer a *behaviour* grader**
  (run the output and check what it does) over string-match — it's both more
  honest and leak-proof; see `references/behaviour-graders.md` and the leak note
  in `eval-contract.md`.
- **Metrics & axes.** Beyond correctness, what else does the user want graded? A
  common second deterministic metric is **perf** ("time to get results" → grade
  it deterministically from the query/plan cost, with wall-clock as a companion;
  the recipe is in `behaviour-graders.md`). Each metric declares an `axis`
  (quality/cost/perf), a `direction`, and an `optimizer_role` (gate/frontier/
  tiebreak) in `HARNESS.md`.
- **Scenarios.** The N inputs and their expected/ideal outputs, chosen to exercise
  the branch the user suspects is weak. Make a **mix**: rows with `expected` are
  deterministic; rows without are open-ended, rubric-only. Escalate difficulty so
  each scenario pins one competency.
- **Rubric dimensions.** What a script can't cheaply check but the user cares
  about (followed the method, handled the edge case for the right reason, stayed
  economical). Co-write dimensions, weights, 0–1 anchors.
- **Agents + N + models.** Which CLIs to fan out over, runs per variant, pinned
  models (so cost is comparable across rounds).
- **Exit criteria.** `target_quality`, `max_rounds`, `patience` — the optimizer's
  stop predicate; record them in the manifest as the recommended defaults.

## 3. Build the deterministic grader (`eval.py`)

Start from `templates/eval.py` (string-match scaffold) and **rewrite the check**
for this skill's notion of correct. The grader is **pure** — no LLM, no network,
no clock, no randomness — so every cohort is judged identically. CLI contract
(enforced by `validate-harness.py`):

```
eval.py --runs <runs.jsonl> --scenarios <scenarios.jsonl> --skill <name> --metric <id> --out <scores.jsonl>
eval.py --runs ... --metric <id> --explain      # human table
```

- Dispatch on `--metric`: one branch per deterministic metric you declared
  (`exact`, `perf`, …). Each emits annotate-shaped rows, score ∈ [0,1].
- A run that errors scores `0.0`; never crash the grader.
- Input-only scenarios emit no deterministic-metric row.

For behaviour graders (execute SQL + diff rows, build + check exit code, validate
schema) and the perf recipe, copy the patterns in `references/behaviour-graders.md`.

## 4. Build fixtures (if the grader needs them)

If correctness/perf is judged by *running* the output against data, that data is
part of the frozen harness. Build it from a **deterministic seed script** (fixed
RNG seed, no clock) so anyone reproduces byte-identical fixtures, and commit the
builder beside the fixture. A perf grader wants a *large, indexed* fixture so the
latency signal is real; correctness wants a *small, hand-verifiable* one. Compute
and **verify every golden against the fixture** before freezing — a wrong golden
poisons the frozen grader.

## 5. Write `scenarios.jsonl`, `rubric.yaml`, and `HARNESS.md`; validate; freeze

- `scenarios.jsonl` — from `templates/scenarios.jsonl`. `input` is the only field
  ever sent to the agent; `expected` is used out-of-band. Omit `expected` for
  rubric-only rows.
- `rubric.yaml` — from `templates/rubric.yaml`. Replace the generic dimensions
  with the ones co-designed in step 2.
- `HARNESS.md` — from `templates/HARNESS.md`. This is what makes the optimizer
  skill-agnostic: declare every metric's axis/direction/role, the cost source,
  agents/N/models, exit thresholds, and required fixtures.

Then **validate conformance** before freezing:

```
python3 scripts/validate-harness.py --skill <name> --eval <skill-dir>/eval
```

It checks the four required files exist, `HARNESS.md` parses, `eval.py` answers
`--metric` for each declared metric, and every fixture is present. Fix anything
it flags — the optimizer runs the same check and refuses a non-conformant `eval/`.

**Confirm the eval logic and rubric with the user**, then freeze by publishing the
instrumented baseline:

```
qvr edit <skill>                       # (skip if already in edit mode)
# place the validated files at <skill-dir>/eval/
qvr publish <skill> --tag <baseline-tag> -m "freeze graded harness" --auto-commit
# path-B first publish needs a remote: add --fork file:///…/<name>.git --migrate
```

This published version is the **instrumented baseline** — the input to
`optimize-skill-loop`. Install it for every fan-out agent so the optimizer can
attribute every run:

```
qvr add <name> --target claude,codex
```

## 6. Hand off to the optimizer

```
# the loop reads <skill>/eval/HARNESS.md and never touches eval/ again
optimize-skill-loop   (point it at <skill>; it validates the contract, then runs)
```

From here the harness is frozen. If you discover the eval itself is wrong, you do
**not** patch it mid-loop — that voids every prior cohort. Come back to this skill,
fix the harness, and re-freeze as a new baseline (the loop restarts).

## Caveats

- **Never put `expected` in the prompt.** The runner sends only `input`; grading
  is out-of-band. The one residual leak is a *pure string-match* grader whose
  scenarios sit on disk — prefer a behaviour grader, or hold those `expected`s in
  the outer run dir (see `eval-contract.md`).
- **The grader must be pure.** Wall-clock, RNG, network, or LLM calls inside
  `eval.py` break reproducibility. Perf "time" is graded via a deterministic
  plan/cost proxy; raw ms is only a reported companion.
- **Freeze means freeze.** Adapting the harness after the loop starts moves the
  goalposts and invalidates the before/after. Re-freeze as a new baseline instead.

See `references/eval-contract.md` for the full interface and
`references/behaviour-graders.md` for ready-to-adapt grader recipes (text2sql
execute-and-diff, perf via EXPLAIN, schema-validate, build-exit-code).
