# Grading: two frozen dimensions → one (quality, cost) vector

Quality has two parts; keep them separate so you can see *why* a variant moved.

## 1. Deterministic (eval.py) — the spine

`eval.py` is a pure function of (runs, scenarios): exact/normalized match,
score ∈ {0,1} per run, emitted as `--metric exact`. No LLM, no flakiness. This
is the dimension you trust most and the one the loop is primarily optimizing.
Prefer designing scenarios so the *target defect* is caught here.

`scenarios.jsonl` is a **mix**: rows carrying an `expected` are the i/o
(deterministic) cases graded here — including **regression anchors** that pin a
bug you already fixed so it can't silently come back; rows with **no** `expected`
are input-only and graded by the rubric only, so `eval.py` skips them. The
`expected` is never sent to the agent (the runner sends `input` only), and the
whole `eval/` dir ships frozen *inside* the skill so it versions with it.

## 2. Rubric (you, the LLM-judge) — the conscience

For what a script cannot cheaply verify (did it follow the method, handle the
edge case for the right reason, stay economical), you judge each session against
the **frozen** `rubric.yaml`, score each dimension ∈ [0,1], aggregate by the
declared weights, and annotate one `--metric rubric` number per run.

Read the session verbatim first: `qvr audit export --session <id> --source transcript`.
Score strictly against the anchors — do not invent criteria mid-loop. If you feel
the rubric is wrong, that is a signal to **stop the loop and restart with a new
frozen rubric (version: 2)**, not to drift it silently.

## Cost axis

From `qvr audit sessions --limit 0 --output json`: per-session `agent_name`,
`turns`, tool-call count, token totals, `working_directory`, and `skill_versions[]`
(the content version it ran: `{skill, version, commit, subtree_hash}`). The runner
threads **no** session id and **no** timing, so `report.py` attributes these to each
(agent, version) cohort by the `subtree_hash` in `skill_versions[]` — folded onto the
runner's version tag and scoped to the project — and averages `tokens_out`. Cost is
what makes this a *pareto* loop instead of a quality-at-any-price loop. (If a cohort
matches no session, `report.py` warns rather than silently scoring it free — usually
means `discover` wasn't run, or that cohort's version came back commit-only.)

## The cohort row

One row per (version, agent):

```
{ version, agent,
  quality_exact:  mean of eval.py scores,
  quality_rubric: mean of your rubric aggregates,
  tokens, turns, tools: from audit sessions }
```

Quality for the pareto plot is usually `quality_exact` with `quality_rubric` as a
tie-breaker — exact is the harder currency. Plot (quality, −cost) per agent; keep
the frontier.

## The freeze contract (non-negotiable)

The candidate diff in step 5 may change the inner skill's **behaviour files
only**. It must never touch `eval/scenarios.jsonl`, `eval/eval.py`, or
`eval/rubric.yaml`. If a round needs a new scenario or a different rubric, the
old comparison is void — bump the rubric `version:`, archive the prior cohorts,
and start a fresh loop. Goalpost-moving is the one way this harness lies.
