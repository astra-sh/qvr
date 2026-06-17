# CI recipe: the outer loop on a schedule

qvr is the substrate — capture, the eval gate, and lineage — not a scheduler or
an agent runner. To run the self-improvement loop unattended you wire two CI
jobs around qvr's CLI. Both are illustrative GitHub Actions; adapt to any
scheduler + cloud-agent platform.

## Job A — PR regression gate (run on every pull request)

Fails the PR when a changed skill's evals regress. This is the cheap, always-on
half: it never edits anything, it just runs the deterministic gate.

```yaml
name: skill-evals
on: pull_request
jobs:
  eval-gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: curl -fsSL https://quiver.sh/install | sh    # install qvr
      - run: qvr audit enable && qvr audit discover
      # Grade each skill that ships an evals.yaml; non-zero exit fails the job.
      - run: |
          for s in $(qvr ls --output json | jq -r '.[].name'); do
            qvr ops eval run "$s" || exit 1
          done
```

The gate grades the skill's most recent **captured** run — so the PR's CI must
first exercise the skill (your existing test/integration step), then
`qvr audit discover` captures it, then `qvr ops eval run` judges it. No agent is
run by qvr itself.

## Job B — scheduled improver (run on a cron)

Proposes improvements. This half needs an **agent** (a cloud coding agent / the
`evolve-skill` loop) and a **scheduler** — both outside qvr. The agent drives the
exact CLI loop from `SKILL.md`: observe → update → roll out → evaluate → open a
PR.

```yaml
name: evolve-skills
on:
  schedule:
    - cron: "0 6 * * *"      # daily; tune to how fast feedback accrues
jobs:
  evolve:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: curl -fsSL https://quiver.sh/install | sh
      - run: qvr audit enable && qvr audit discover
      # Hand the loop to your agent runner. It reads `qvr audit annotations` +
      # `qvr audit export`, edits the skill behind `qvr edit`, re-runs it,
      # gates on `qvr ops eval run`, and opens a PR. It MUST NOT merge.
      - run: your-agent-runner run --skill evolve-skill --target "$SKILL"
        env:
          SKILL: triage-issue
```

## Rules the scheduled job must honor

- **Open a PR; never merge.** A passing eval clears a change for human review,
  not for an automatic landing. The job's output is a PR, full stop.
- **Never auto-install.** Do not `qvr add` the candidate into anyone's project as
  part of the loop.
- **Gate every change.** No PR without a `qvr ops eval run` pass recorded for the
  candidate's commit — confirm with `qvr ops promote <skill>` (non-zero exit
  blocks).
- **No AI attribution in the PR.** Title and body describe the change and the
  before→after eval score; they carry no assistant byline or footer.

## What qvr provides vs what you provide

| Step | Provided by |
|------|-------------|
| Capture runs, lock-verified | **qvr** (`audit discover`) |
| Human + auto feedback | **qvr** (`audit annotate` / outcome) |
| Eval gate, keyed to commit | **qvr** (`ops eval run`) |
| Lineage (score over commits) | **qvr** (`ops lineage`) |
| Promotion gate | **qvr** (`ops promote`) |
| Scheduling (cron) | **you / CI** |
| Running the agent + drafting the diff | **you / a cloud agent** |
| Opening the PR | **you / CI** |
