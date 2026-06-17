---
name: evolve-skill
description: >
  Runs a self-improvement loop on an installed skill: read what it actually did
  (auto outcomes + human verdicts), draft a fix to its SKILL.md, and keep the fix
  only if qvr's eval gate shows the score improve. Use when a user wants a skill
  to get better from feedback over time — e.g. "improve my triage skill from the
  feedback", "evolve this skill", "close the loop on skill quality", "auto-tune a
  skill against its evals", or "why did this skill regress and how do I fix it".
  Drives qvr audit + qvr ops; never auto-installs or auto-merges — it opens a PR
  for a human to merge. Experimental.
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# Evolve a skill with qvr's eval gate

This is the **outer loop** of skill self-improvement, run as an agent over qvr's
CLI. The **inner loop** already happened: agents used the skill, and qvr captured
every run — lock-verified to the exact installed commit — via `qvr audit`. Your
job here is to read those runs plus any human verdicts, draft an improvement to
the skill's `SKILL.md`, and **prove it helps before it ships** by re-running the
skill and grading it with `qvr ops eval`. A candidate that doesn't improve the
eval score is discarded; one that does is opened as a pull request for a human to
merge.

The discipline is deliberate: qvr is **evidence-gated**. Skills do not auto-evolve
from raw model confidence — every change passes a deterministic eval keyed to the
skill's locked commit, because curated-with-evidence beats blind self-generation.

## When to use this

- A skill has been running for a while and the user wants it to improve from how
  it actually performed (failures, human corrections), not from a guess.
- The user has recorded verdicts with `qvr audit annotate` and wants them acted on.
- A skill regressed after an edit and the user wants the loop to find and fix it.

This **changes a skill's content** behind an eval gate. It is not observability
(that's `trace-skill-activity`), supply-chain verification
(`verify-skill-supply-chain`), or install management (`onboard-skills`).

## Prerequisites

- `qvr audit` capturing the target skill's runs (`qvr audit enable` + `discover`;
  see `trace-skill-activity`).
- An **`evals.yaml`** beside the skill's `SKILL.md` — qvr's own manifest of
  suites/cases graded deterministically over the captured trace (no model calls).
  If the skill has none, write one first: encode the behaviors that matter,
  starting with any run a human flagged as wrong (a regression case).

A minimal `evals.yaml`:

```yaml
version: 1
suites:
  - name: triage-correctness
    cases:
      - name: ambiguous-feature-needs-info
        graders:
          - type: outcome          # the captured run did not error
            expect: success
          - type: text             # and produced the right label
            on: final_message
            contains: ["needs-info"]
            reject: ["ready-to-implement"]
```

Grader types available (all deterministic, all over the trace qvr already has):
`outcome`, `text`, `tool_sequence`, `tool_constraint`, `skill_invocation`,
`behavior`. Semantic ("is this explanation clear?") judgement is **not** a core
grader — delegate that to a judge skill and fold its verdict back in as an
annotation; keep the gate itself model-free.

## The loop

### 1. Observe — read how the skill actually did

```
qvr audit logs --kind SKILL --since 30d        # where the skill fired
qvr audit sessions --skill <name>              # its runs, with auto outcomes
qvr audit annotations --skill <name>           # human verdicts ("bad: …, because …")
qvr audit export --session <id> -o run.jsonl   # one run verbatim, for close reading
```

Auto **outcome** (`success`/`failure`/`blocked`) tells you *that* a run went
wrong; a human **annotation** tells you *why*. Cluster the failures + notes into
one concrete weakness — the smallest change that would have fixed the flagged
runs. Record a verdict yourself if one is missing:

```
qvr audit annotate <session-id> --skill <name> --outcome bad --note "why it was wrong"
```

### 2. Update — draft the fix

Eject a writable copy and edit it:

```
qvr edit <name>                 # promote the symlinked skill to a real dir
# …edit <dir>/SKILL.md to address the weakness…
qvr diff <name>                 # review the change
```

Make the **smallest** change that addresses the observed failures. Add the
flagged input as a regression case in `evals.yaml` if it isn't already one.

### 3. Roll out the candidate — generate fresh evidence

Run the edited skill on the held-out / regression inputs (this is a real agent
run — execution stays with the agent, not qvr), then let qvr capture it:

```
# …invoke the skill on the regression input(s)…
qvr audit discover --agent <agent>   # capture the fresh run(s)
```

### 4. Evaluate — the gate

```
qvr ops eval run <name> --suite <suite>
```

This grades the skill's most recent captured run against the suite, **keyed to
the locked commit**, and exits non-zero if any case fails. **Keep the edit only
if the score strictly improved** — i.e. cases that failed before now pass. If it
did not improve, discard the change (`git checkout` the ejected dir or revert)
and go back to step 1 with what you learned. Inspect the trail any time:

```
qvr ops lineage <name>          # eval verdicts + annotations over commits
```

### 5. Ship — open a PR, never merge

Once the gate passes, publish/commit the change on a branch and **open a pull
request for a human to review and merge**. Do **not** merge it yourself, and do
**not** auto-install the new version — a passing eval clears it for review, not
for an automatic landing. Summarize: the weakness observed, the change made, and
the eval score before → after.

```
qvr ops promote <name>          # confirms the locked commit has a passing eval
```

`qvr ops promote` is the gate made explicit: it refuses (non-zero exit) unless
the locked commit is backed by a passing eval, so a CI step or the loop can
branch on it.

## Running it unattended

The same loop runs on a schedule via CI — a scheduled job that proposes a PR,
plus a PR check that fails when `qvr ops eval run` regresses. See
[`references/ci-recipe.md`](references/ci-recipe.md). The schedule and the agent
runner live outside qvr (qvr is the substrate, not a scheduler); qvr provides the
capture, the gate, and the lineage.

## Gotchas

- **Evidence-gated, always.** A change ships only behind a passing eval keyed to
  its commit. No eval improvement ⇒ no change. Resist "the model is confident."
- **Never auto-merge, never auto-install.** Open a PR; a human merges. A passing
  eval is a green light to *review*, not to land.
- **Annotations are the "why."** Auto outcome alone can't tell a correct-but-
  unhelpful run from a good one; the human verdict carries the intent the loop
  optimizes toward.
- **Keep the gate model-free.** Deterministic graders over the real trace are the
  core; push LLM judgement to a separate judge skill so the gate stays
  reproducible and cheap.
- **Experimental.** `qvr ops` / `qvr audit` command names and the `evals.yaml`
  schema may change; pin your qvr version if you script the loop.

## Troubleshooting

- *`skill … has no evals.yaml`* — write one beside `SKILL.md` (see Prerequisites).
- *`no captured sessions for skill … to evaluate`* — run the skill, then
  `qvr audit discover`, before `qvr ops eval run`.
- *The eval passes but the skill still feels wrong* — your suite is under-
  specifying the behavior. Add the failing case; the loop is only as good as its
  evals.
- *Lineage shows no commit move* — the edited skill wasn't re-committed, so the
  eval is keyed to the same commit. Commit the change (or `qvr edit`/`publish`)
  so the gate distinguishes before from after.
