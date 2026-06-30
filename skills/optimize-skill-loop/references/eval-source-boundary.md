# The eval-source boundary — `session → metrics`, qvr is the ledger

`optimize-skill-loop` owns the **outer loop only**: qvr versioning, the
iterate→fix→publish cycle, the cross-version pareto frontier, the deterministic
stop predicate, and the before/after lineage. It does **not** author evals and
does **not** define how a run is graded. Those belong to an **eval source**
upstream of the loop.

```
        EVAL SOURCE (upstream)                   optimize-skill-loop (this skill)
  ┌──────────────────────────────┐        ┌──────────────────────────────────┐
  │ freezes an eval definition    │        │ qvr switch <skill> <version>     │
  │ that rides with a qvr-        │  ───▶  │ run a cohort (fresh sessions)    │
  │ versionable skill, and maps   │        │ qvr audit discover  (→ ledger)   │
  │ each run → metrics ∈[0,1]     │        │ join sessions ⋈ metrics by version│
  └──────────────────────────────┘        │ pareto + stop predicate + report │
            ▲                              └──────────────────────────────────┘
            │  the seam below is ALL the loop needs from a source
            ▼
                    ┌───────────────────────────┐
                    │  qvr  — the inner ledger   │
                    │  owns sessions, cost,      │
                    │  per-version identity      │
                    └───────────────────────────┘
```

## The whole contract in one line

> **A run is a qvr session; an eval source is anything that turns each session
> into metrics.** The loop joins the two by the version it switched to and judges
> the frontier. Everything below is detail on those two halves.

If a source can satisfy the two parts below, the loop runs over it unchanged. It
never learns what the skill *does* — text2sql, slugify, a codemod all look
identical from up here.

## qvr is the inner ledger (the spine — never re-implement it)

Every headless run the cohort spawns becomes a **qvr session** after `qvr audit
discover`. That session — not anything the runner mints — is the unit of
attribution. qvr owns, per session:

- the **session id** (agent-native; the runner mints nothing),
- **cost** — tokens / turns / tools / duration,
- the **skill content identity it ran** — `skill_versions[] = {skill, version,
  commit, subtree_hash}`. `subtree_hash` is the capture-stable join key: both
  agents of one version share it and it never moves on switch/edit.

So "attribution through the qvr session" means: each score attaches to the
**cohort of sessions** that ran a given content version, found by the
`subtree_hash` stamped on each session (scoped to the loop's project dir). No
run-key, no wall-clock window, no `correlate.py` — that whole layer is gone. The
loop and the source both read the ledger; neither rebuilds it.

## Part 1 — an eval definition that rides with a qvr-versionable skill

The source freezes its eval (scenarios / evaluator / rubric / fixtures, in
whatever form it likes) so it **versions along with the inner skill** under qvr
(`edit`/`publish`/`switch`/`version list`). The loop's only requirement: it can
`qvr switch <skill> <version>` before a cohort so every run in that cohort
attributes to that one content version. How the eval is stored is the source's
business; that it rides the skill's version is the contract.

The native source (`create-skill-eval`) realizes this as a frozen `<skill>/eval/`
directory. Another source could store its suite anywhere that travels with the
skill subtree — the loop only ever calls `switch`.

## Part 2 — `session → metrics`: the adapter

For each cohort the source (or an adapter the optimizer ships for it) maps the
cohort's **sessions** to **metric scores**, emitting **one normalized row per
run**:

```json
{
  "version": "v0.3.0",
  "agent":   "claude",
  "model":   "claude-sonnet-4-6",
  "scenario":"s5_join",
  "run":     1,
  "session": "<qvr session id — optional; see below>",
  "metrics": {"exact": 1.0, "perf": 0.66, "rubric": 0.8},   // each declared metric, ∈[0,1]
  "cost":    {"tokens_out": 245, "turns": 1, "tools": 5, "duration_ms": 1234},
  "companions": {"perf_latency_ms": 0.97}    // optional human/plot-only numbers
}
```

- **`version`** is the join key the loop owns — the content version it switched to
  before this cohort. The report aggregates per (agent, version) from this field,
  never from a session id (an agent may reuse/collapse its native id across runs).
- **`metrics`** carries every metric the manifest declares (gate + frontier +
  tiebreak), each ∈ [0,1]. Input-only scenarios omit the deterministic metrics and
  carry only `rubric`.
- **`session`** is **not minted by the runner** — qvr owns ids. Fill it only when a
  metric needs the transcript (e.g. an LLM-judge rubric reads the session, then
  `qvr audit annotate`s the score back onto that session id). Deterministic quality
  (`output` vs `expected`) and cost need **no** session id; they attribute by
  `version` and `subtree_hash` respectively.
- **`cost`** is the ledger view, pulled from `qvr audit sessions` and attributed to
  the cohort by `subtree_hash` (the session identity above). The source does not
  measure cost itself — it reads it from qvr.
- **`companions`** are reported but never feed the pareto decision (e.g. raw
  latency ms behind a deterministic `perf` score).

That is the entire seam. A source that can produce these rows — *by any means* —
plugs into the loop. The native adapter (`adapters/native.md`) is one concrete
implementation (`run-cohort.sh` + `eval.py` + the ledger); a foreign runner that
already emits its own per-run scores conforms by normalizing its output into these
rows and tagging each with the `version` the loop switched to.

## The manifest drives the frontier (N-axis, generic)

The optimizer reads the source's manifest (native: `eval/HARNESS.md`) to learn the
metrics and their `axis`/`direction`/`optimizer_role`, then builds the frontier
generically:

- every `gate`/`frontier` metric + the `cost` axis become frontier axes;
- a variant is **dominated** iff another beats-or-ties it on every axis with one
  strict — kept variants form the frontier;
- the **gate** metric is what `--target-quality` is checked against in the stop
  predicate; `tiebreak` metrics (e.g. `rubric`) only break frontier ties.

> Implementation note: `report.py` ships the quality×cost frontier; when a manifest
> declares additional frontier metrics (e.g. `perf`), wire them in as extra axes
> there — the decision rule and tie-breaks generalize unchanged. A manifest with
> only an `exact` metric runs as the original 2-axis loop. A non-native source
> supplies the same `axis`/`direction`/`optimizer_role` triple in whatever manifest
> form it uses; the loop only needs those three fields per metric.

## Aggregation (per version, per agent)

From the normalized rows the optimizer computes, for each (version, agent):

- `quality` = mean of the **gate** metric over the `expected` (i/o) scenarios;
- each other frontier metric = mean over its applicable scenarios;
- `cost` = mean `cost.tokens_out` (read from the ledger, joined by `subtree_hash`);
- `rubric` = mean rubric score (tiebreak).

These per-(version, agent) points are what the pareto frontier and the report plot
are built from. The loop keeps the frontier-advancing version and switches to it;
it **never** lets the eval source declare the winner.

## Conformance is enforced, not assumed

Before the first cohort the loop validates the source against the contract and
refuses a non-conformant one (native: `create-skill-eval/scripts/validate-harness.py`
— required files present, manifest parses, the grader answers every declared
metric, fixtures present). A foreign source ships an equivalent check or declares
itself conformant by emitting the rows above; the loop will still refuse to start
if a cohort can't be graded.

## Adapters in this skill

- `adapters/native.md` — the native source: `create-skill-eval`'s frozen `eval/`,
  `run-cohort.sh`, `eval.py`, and the ledger. **Default, zero external dependency**,
  and the only adapter shipped here.
- To add a foreign source, drop a self-contained `adapters/<name>.md` beside it that
  documents how its runner produces the Part-2 rows (and how it freezes its eval to
  ride the skill version, Part 1). The optimizer selects an adapter by config and
  otherwise stays entirely source-agnostic — nothing else in the loop changes.
