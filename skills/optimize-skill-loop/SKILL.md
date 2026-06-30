---
name: optimize-skill-loop
description: >-
  Deterministic outer-loop optimizer for a skill that already carries a frozen
  eval (produced upstream by an eval source such as create-skill-eval). Runs the
  frozen eval across user-chosen agents on fresh headless sessions via a pluggable
  adapter, grades quality/cost/perf, keeps the pareto-best variant, and versions
  every iteration with qvr. Use when a skill is already instrumented and you want
  a reproducible, article-grade before/after. (To BUILD the eval first, use
  create-skill-eval.) Trigger phrases: "run the skill optimization loop", "optimize
  <skill> across agents", "grade and evolve <skill> across versions".
allowed-tools:
  - shell        # run-cohort.sh / grade-cohort.sh drive headless agent + qvr CLIs
  - exec         # agent invocations + qvr audit subprocesses
  - file_read    # read run records, scenarios, captured sessions
  - file_write   # write run/score records, the report json + svg
  - network      # spawned agent CLIs + qvr audit reach the network
metadata:
  author: quiver-playground
  version: "2.1.0"   # qvr 0.30.x: cost attributes by skill_versions subtree_hash (no window); runner owns version-tag only
---

# optimize-skill-loop

A deterministic harness for improving one inner skill on a feedback loop.

- **You (the agent) are the outer loop** — the brain, the judge.
- **qvr is the ledger and the session bookkeeper** — it spawns nothing, but as of
  0.30.x it derives clean per-agent spans for **every** agent (claude, codex, …),
  so it captures every fresh headless run, owns its **session id**, attributes it
  to the skill content version (`subtree_hash`), records its **cost**, and buckets
  the cohorts. You no longer mint run-keys or correlate session ids — that whole
  layer is gone; a **naked** headless run is fully attributed by qvr.
- **The inner loop is fresh, no-leak headless agents** (`claude -p`, `codex`,
  `cursor`, `gemini`, …) — each one a clean process running the inner skill on a
  fixed scenario, so no cross-run context bleeds the comparison.

> **What the loop still owns** (qvr can't infer it): the **version tag** it ran
> (set via `qvr switch` before the cohort) — the candidate's identity, and the key
> quality joins on. That is *all*. The runner records **no** session id, **no**
> timing, **no** window: qvr stamps every session with the skill content version it
> ran (`subtree_hash`), so the grader attributes a cohort's ledger sessions by skill
> **identity**, not by a wall-clock window. The outer loop does only **evals +
> candidate generation**; every bit of session/cost/attribution bookkeeping is qvr's.

The whole point of this skill over an ad-hoc loop is **determinism**: the grader
is frozen *before* the loop starts and never moves, so every cohort is judged by
the identical yardstick. You only ever change the inner skill — never the rubric,
never the evaluator, never the scenarios.

**This skill sits downstream of an eval source, and is source-agnostic.** It does
**not** author the eval or define how a run is graded. The contract it consumes is
one line: **a run is a qvr session; an eval source is anything that turns each
session into metrics ∈[0,1].** Any upstream source that (1) freezes an eval which
rides the skill's qvr version and (2) maps each cohort run → metrics plugs in
unchanged — the native one is `create-skill-eval`, but a foreign runner that
already emits per-run scores conforms by normalizing them into the same rows. The
loop consumes that normalized cohort, joins it to the ledger by the version it
switched to, and owns only the outer machinery. The full seam — the qvr-ledger
spine, the `session → metrics` row schema, and the adapter interface — is in
`references/eval-source-boundary.md`.

## Division of labor (do not blur these)

| Eval source owns (upstream) | You (the loop) own | qvr owns |
|---|---|---|
| Authoring + freezing the graded harness | Switching the version before each cohort | Capture + **session ids** (`audit discover`) |
| `run()` adapter: run+grade a cohort | Aggregating → (quality, cost, perf) vectors | Per-agent attribution + **cost** (`audit sessions`) |
| Scenarios / evaluator / rubric / fixtures | Computing the pareto frontier, picking survivors | Version cohort by `subtree_hash` (`audit compare`) |
| The manifest (metrics, axes, agents, N) | The deterministic stop predicate + report | Versioning each variant (`edit`/`publish`/`switch`) |
| | **(version-tag)** per cohort — the candidate id, nothing else | Cost + timing + the version each session ran; before/after evidence (`audit compare`/`export`) |

> **qvr never declares a winner.** `qvr audit compare`'s own help says so: it
> buckets and shows; you judge. qvr **does** now own session ids, per-agent
> attribution, cost, and the version cohort (keyed by `subtree_hash`). The only
> bookkeeping left to you is the trivial **version tag** per cohort — the candidate's
> identity; the grader attributes ledger sessions to it by `subtree_hash`.
>
> **Caveat (qvr 0.30.x) — now scoped to `export`/`spans` only:** the human-readable
> version *label* (`ref`/`commit`/`skill.version`) in **`spans`/`export`** is still
> resolved **live against the currently checked-out worktree** — only the checked-out
> version is labeled there; other cohorts read `subtree_hash` only (`ref=""`).
> **`audit sessions` and `audit compare` are fixed:** each cohort now carries its
> **frozen, switch-invariant** label — `sessions[].skill_versions[]` = `{skill,
> version, commit, subtree_hash}`, and `compare` populates `ref` per cohort — so you
> can trust those labels regardless of checkout. `subtree_hash` is still the
> capture-time-stable identity and the join key. See `references/audit-cheatsheet.md`.

## The five frozen invariants (owned by the eval source — you only read them)

These are frozen **upstream** by the eval source before the loop starts, and the
loop treats them as read-only. If any changes mid-loop, the goalposts moved and
every prior cohort is invalid:

1. The **scenario set** (`<skill>/eval/scenarios.jsonl`) — N fixed cases. A
   **mix**: rows with `expected` are i/o (deterministic) cases; rows without are
   input-only, judged by the rubric.
2. The **deterministic evaluator** (`<skill>/eval/eval.py`) — per-metric grade ∈ [0,1].
3. The **rubric** (`<skill>/eval/rubric.yaml`) — fixed LLM-judge dimensions + weights.
4. The **agent set** + **N** (runs per agent per variant) — from the manifest.
5. The **task prompt template** the headless runner sends — from the manifest.

The loop **never edits `eval/`**. Only the inner skill's behaviour files (its
`SKILL.md`, scripts, references) change between rounds; `git diff <baseline>
<candidate> -- '*/eval/*'` must stay empty. If the eval itself is wrong, stop,
fix it with the eval source (`create-skill-eval`), and re-freeze a new baseline.

## 0. Prereq (once per machine)

```
qvr audit enable        # capture on; reads agents' own session stores, no agent config touched
```

Requires **`python3`** on PATH — the runner, grader, and the frozen `eval.py`
all use it (stdlib only, no `pip install`). It is the skill's one prerequisite
beyond `qvr` and the agent CLIs you fan out across.

## 1. Get an instrumented skill from an eval source

The loop optimizes one inner skill that **already carries a frozen `eval/`** with
a manifest (`eval/HARNESS.md`). If the skill isn't instrumented yet, build the
eval upstream first with **`create-skill-eval`** (the native source) — it
scaffolds a minimal baseline, co-designs scenarios/evaluator/rubric/fixtures with
the user, validates conformance, and publishes the instrumented baseline. A
non-native source plugs in through its own adapter (see
`references/adapters/`); the loop stays source-agnostic.

Confirm the skill is instrumented and installed for **every fan-out agent** (an
agent only attributes a skill in its own catalog):

```
qvr list                                   # shows the instrumented skill + version
qvr add <skill> --target claude,codex      # install for the manifest's agent set
```

## 2. Pick the adapter and validate the contract

Select the eval source's adapter (default: the **native** adapter,
`references/adapters/native.md`) and validate the source conforms before any
cohort — the loop refuses a non-conformant `eval/`:

```
python3 .claude/skills/create-skill-eval/scripts/validate-harness.py \
  --skill <skill> --eval .claude/skills/<skill>/eval
```

Read the manifest (`eval/HARNESS.md`) for the frozen run config — metrics + their
axes/roles, agents, N, pinned models, and the exit thresholds (`target_quality`,
`max_rounds`, `patience`). These drive the cohort and the frontier; you do not
re-decide them. The full seam is `references/eval-source-boundary.md`.

The loop keeps only its *transient* outputs outside the skill (never the cases):

```
mkdir -p run/{runs,scores}             # run records + scores (gitignore or commit to the DEV repo)
```

## 3. Run a cohort (the inner loop) — via the adapter

Hand the **current version** to the eval source's adapter; it runs the frozen
scenarios across the frozen agent set, N times each, on **fresh headless
sessions**, grades them, and returns the normalized cohort. With the **native**
adapter (`references/adapters/native.md`) this is the deterministic runner — the
grader rides inside the skill at `.claude/skills/<skill>/eval/` (the consume-mode
symlink points at the current version's frozen copy); the runner reads
`scenarios.jsonl` there but sends only each row's `input`:

```
EVAL=.claude/skills/<skill>/eval       # frozen grader dir, in-skill, current version
bash scripts/run-cohort.sh \
  --skill <skill> --version <current-tag> \
  --scenarios "$EVAL/scenarios.jsonl" \
  --agents claude,codex --n 1 \
  --out run/runs/<current-tag>.jsonl
```

(A non-native source runs its own adapter from `references/adapters/` and
normalizes into the same rows — see `references/eval-source-boundary.md`. The rest
of the loop is identical and source-blind.)

Each line of `run/runs/<tag>.jsonl` is one **naked** run: `{agent, scenario_id,
input, output, version, project, model}` (plus an empty `session`, schema-compat
only). There is **no** session id, **no** run-key, and **no** timing/window — the
runner mints nothing. qvr owns session ids, cost, and timing; the runner owns only
the version tag (the candidate's identity) and its project dir. See
`references/headless-runner.md`.

Then let qvr ingest the sessions — one call, both agents, no correlation step:

```
qvr audit discover --agent claude,codex     # ingests claude + codex native sessions
```

That's it. qvr 0.30.x derives clean spans for every agent, so a naked run lands
with `agent_name`, `skills=[<skill>]`, tokens, and its `skill_versions[]` — the
content version it ran, `{skill, version, commit, subtree_hash}` — no
`[qvr-run-key:]` tag, no `correlate.py`. The grader (step 4) attributes these
sessions to the cohort by **skill identity** (the `subtree_hash` in
`skill_versions[]`), scoped to the loop's project dir.

> **`switch <v> → run → discover`, one version at a time.** Cohorts no longer need
> distinct wall-clock windows: qvr tags each session with the `subtree_hash` it ran,
> so two versions in the **same** project (even overlapping in time) stay cleanly
> separated by content identity. Sequential is still the tidiest operative order —
> it is no longer a correctness requirement of the cost attribution.

## 4. Grade the cohort (frozen graders)

**Deterministic dimensions** — grade *every* metric the manifest declares
(`exact`, `perf`, …) with one helper. It reads the metric ids from `HARNESS.md`,
runs `eval.py` per metric (a pure function of `output` vs `expected` — **no session
ids needed**), and **stamps each score row with the version**:

```
bash scripts/grade-cohort.sh --skill <skill> --version <current-tag> \
  --runs run/runs/<current-tag>.jsonl --eval "$EVAL" --out-dir run/scores
# writes run/scores/<current-tag>.<metric>.jsonl, each row tagged {…, "version": "<tag>"}
```

> Version-tagging is the join key: `report.py` aggregates quality per (agent,
> version) **from the score rows' `version` field**, never through a session id.
> This is the loop owning the version→runs mapping via the tag it switched to.

**Rubric dimension** — you are the LLM-judge, applying the *fixed* `rubric.yaml`
to each captured session verbatim. qvr supplies the session ids — list the cohort's
sessions, then read + score each:

```
# the cohort's sessions (qvr owns the ids): this project, this skill, this version
qvr audit sessions --since 1h --output json     # filter to working_directory + agent + skill_versions[].subtree_hash
qvr audit export --session <id> --source transcript    # the session as qvr loaded it
# score each rubric dimension ∈ [0,1] strictly per rubric.yaml anchors, then annotate
# back onto the qvr session id (so it surfaces as compare's SCORE):
qvr audit annotate <id> --skill <skill> --metric rubric --score <0..1> --grader llm-judge --agent <agent>
```

**Cost dimension** — from the ledger, attributed by skill identity (no guessing, no
session-id plumbing in the runner):

```
qvr audit sessions --since 1h --limit 0 --output json   # per-session turns/tools/tokens + agent + skill_versions[]
# report.py attributes these to each (agent, version) by the subtree_hash in
# skill_versions[] (scoped to the project dir) — no wall-clock window
```

Record one **cohort row** keyed by the version you ran: `{version, agent,
quality_exact, perf, quality_rubric, tokens, turns, tools}` — the normalized
cohort of `references/eval-source-boundary.md`. `quality_exact` is the gate
metric's pass rate over the **i/o rows only**; `perf` (and any other frontier
metric) averages over its applicable rows; input-only rows contribute only to
`quality_rubric` and the cost columns. See `references/grading.md` for combining
dimensions and the freeze contract.

## 5. Diagnose the dominant failure

```
qvr audit logs --skill <skill> --status failure --output json   # errored/blocked runs
python3 "$EVAL/eval.py" --runs run/runs/<current-tag>.jsonl \
  --scenarios "$EVAL/scenarios.jsonl" --metric exact --explain   # which i/o scenarios scored low
qvr audit export --session <id>                                   # read WHY a low run failed
```

Cluster the misses; pick the **single dominant pattern** to fix this round.

## 6. Propose one candidate

```
qvr edit <skill>
# edit ONLY behaviour files (SKILL.md / scripts) to fix the dominant pattern.
# NEVER touch eval/scenarios.jsonl, eval/eval.py, or eval/rubric.yaml.
qvr publish <skill> --tag <next-tag> -m "evolve: <what changed>" --auto-commit
```

`publish` cuts the release, runs the scan, and flips the skill back to consume
mode **at the new tag** — leaving the skill *on* the candidate.

## 7. Evaluate the candidate

Repeat steps 3–4 on the **same** frozen scenarios/agents/N, keyed to the new
version. Then corroborate with qvr (best-effort, never authoritative):

```
qvr audit compare <skill> --output json    # buckets by subtree_hash; SCORE = your annotate mean
```

> `compare` now labels **every** cohort: each carries a populated, switch-invariant
> `ref`/`commit`, so the non-checked-out cohort(s) read their real tag too (not `ref=""`)
> — see the 0.30.x note above. Still join cohorts by **`contentHash` (= `subtree_hash`)**,
> the capture-time-stable identity, rather than by label. Both cohorts are there and
> distinct, and both report correct `sessions` + tokens.

## 8. Pareto decision (you compute it; qvr won't)

- Plot every variant on the frontier axes the manifest declares, **per agent** —
  quality + cost always, plus any extra frontier metric (e.g. `perf`).
- A variant is **dominated** if another is ≥ on every axis and > on at least one
  (quality ↑, cost ↓, perf ↑). Keep the frontier; drop the dominated.
- Keep the candidate iff it joins/advances the frontier; else revert:
  `qvr switch <skill> <kept-ref>` (or `--latest`).

## 9. Loop or stop — a deterministic predicate, not a judgment call

Don't eyeball when to stop. `report.py` (step 10) computes the exit decision from
fixed thresholds, so it's reproducible. The loop **stops** iff any of:

- **every agent ≥ `--target-quality`** (all converged), or
- **`--round` ≥ `--max-rounds`** (hard cap), or
- **`overall_best` unchanged for `--patience` rounds** (no-advance, needs `--history`).

Otherwise it **continues**, and `report.py` names the `next_target` = the open
agent with the most headroom (worst-first, deterministic tie-break by name). So a
per-agent quality below target is *not* an exit — it's the next round's target.

> Example from the dogfood: after round 1, `exit.recommend = "continue — target
> agent: codex"` (claude converged at 1.00; codex open at 0.80, headroom 0.20).
> codex sitting at 0.80 is **best-so-far, not converged** — the loop would target
> codex's `&`-literal failure next (v0.5.0). Stopping there is a manual choice,
> which the predicate flags as premature.

Append this round's `overall_best` to your `--history` file, then continue from
step 5 with `next_target`'s dominant failure, or stop and run the final report.

## 10. Report — the plot across agents and the chosen skill

End every run with the deterministic report. `scripts/report.py` re-grades each
cohort from the run records + the frozen scenarios (a pure function — no LLM, no
clock, no randomness, so the winner and the plot are reproducible), applies the
documented pareto + tie-break rule, and emits both a machine-readable decision and
the plot:

```
qvr audit sessions --since 30h --limit 0 --output json > run/sessions.json   # --limit 0: don't truncate the cohort
python3 scripts/report.py \
  --runs 'run/runs/*.jsonl' --scenarios "$EVAL/scenarios.jsonl" --skill <skill> \
  --sessions run/sessions.json --scores 'run/scores/*.jsonl' \
  --round <n> --max-rounds <m> --target-quality 1.0 --history run/history.jsonl \
  --out-json run/report.json --out-svg run/report.svg
```

The plot (`report.svg`) is itself a frozen-grader output — a pure function of the
run records + scenarios — so it belongs with the rest of the eval record. Keep
`run/report.{json,svg}` + `run/history.jsonl` committed in the **dev repo** (not
inside the published skill) so the before/after is reproducible by anyone; don't
hand-copy the SVG elsewhere.

- **`report.svg`** — quality across agents, one bar per version, the **chosen
  winner starred ★**, overall best in the subtitle.
- **`report.json`** — `winner_per_agent`, `overall_best`, the `exit` block (stop?
  reasons, `next_target`, per-agent `converged`/`open` + headroom), and every
  cohort's `{quality, cost, rubric, frontier, chosen}`.

**The decision rule (deterministic, in `report.py`'s header):** quality = mean
exact pass-rate; cost = mean output tokens/run. A version is on an agent's frontier
if nothing else for that agent is ≥ quality and ≤ cost with one strict; the chosen
winner is the frontier point by (highest quality → lowest cost → highest rubric →
highest version), and the overall best is the version chosen for the most agents
(same tie-breaks). Then `qvr switch <skill> <overall_best>` and report the lineage
(`qvr version list <skill>`).

> **Extension point (N-axis frontier).** `report.py` ships the quality×cost rule
> above. When the manifest declares an extra frontier metric (e.g. `perf`), wire
> it in as another axis: extend the dominance test to require ≥/≤ on that axis too
> and add it to the tie-break order (quality → cost → perf → rubric → version).
> The decision rule generalizes unchanged; a manifest with only `exact` runs as
> the original 2-axis loop. See `references/eval-source-boundary.md`.

## Caveats (so this runs out of the box)

- `qvr audit` is **experimental and opt-in**; pin `--output json` and tolerate
  shape drift. There is **no built-in `promote`** — you switch to the winner.
- **qvr grades nothing.** Quality and the frontier are entirely yours; qvr stores
  runs, versions, and the numbers you hand it via `annotate`.
- `audit` run **status** (`success`/`failure`/`blocked`) means "did it error",
  **not** a quality grade — never treat it as one.
- `publish` refuses a dirty edit: pass `--auto-commit`. It returns you to consume
  mode at the new tag (re-`qvr edit` before the next candidate).
- **The runner mints no session ids, no run-keys, and no timing** — `qvr audit
  discover` owns session ids, cost, and the per-run clock for every agent (claude +
  codex + …). Cohort identity is the **version tag** the runner records; cost/rubric
  attribute ledger sessions by **skill identity** (the `subtree_hash` in
  `skill_versions[]`), scoped to the project dir. No window, no `correlate.py`.
- **Version *labels* are live-resolved in `spans`/`export` only** (qvr 0.30.x): there,
  only the checked-out version is labeled; others read `subtree_hash` only. But
  `audit sessions` (`skill_versions[]`) and `audit compare` (`ref` per cohort) now
  carry **frozen, switch-invariant** labels you can trust off-checkout. `subtree_hash`
  is still the identity and the join key everywhere.
- Cost is attributed by **skill identity** (`subtree_hash`), so two versions can share
  one project dir and even overlap in time without cross-attribution — the old "one
  cohort per window" constraint is gone. If a cohort's cost comes back `None`,
  `report.py` warns (usually `discover` wasn't run after the cohort, or that cohort's
  skill version came back commit-only — the residual ref↔commit skew).
- Keep `eval/` out of the **task prompt** — the runner sends only each scenario's
  `input`, never the `expected`. `eval/` itself ships in the skill (so the grader
  versions with it); the only thing that must not be a plaintext string in the
  worktree is the `expected` of a **pure string-match** scenario (see step 2's note).
