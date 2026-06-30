# The headless inner loop — fresh, no-leak, naked (qvr owns the sessions)

The inner loop is not magic and qvr does not spawn it. It is `run-cohort.sh`
launching one fresh agent process per run. Three properties make the comparison
trustworthy:

1. **Fresh / no-leak.** Each run is a brand-new headless process with no resumed
   context. Nothing from run *k* can bias run *k+1*, so two variants are compared
   on equal footing. Never use `--resume`/`--continue` inside a cohort.
2. **Skill-attributed.** The task prompt names the inner skill and tells the
   agent to read its `SKILL.md`. That genuine load is what `qvr audit` attributes
   to the skill's content version (a SKILL.md tool-call or path-read both count).
3. **Naked.** The runner injects **nothing** into the run for tracking — no
   run-key tag, no caller-chosen session id. qvr 0.30.x derives clean spans for
   every agent, so a bare run is fully attributed after `qvr audit discover`.

## Per-agent invocation (what run-cohort.sh does under the hood)

| Agent | Command | Result extraction |
|---|---|---|
| `claude` | `claude -p "<prompt>" --output-format json --permission-mode acceptEdits` | `.result` from the JSON |
| `codex` | `codex exec --json "<prompt>"` | last `item.completed` with `item.type=="agent_message"` → `.text` |
| `cursor` | `cursor-agent -p "<prompt>"` | stdout (plain) |
| `gemini` | `gemini -p "<prompt>"` | stdout (plain) |

No agent needs a caller-chosen session id. Each mints its own; `qvr audit
discover` reads it back from the agent's native store (Claude Code's
`~/.claude/projects`, Codex's `~/.codex/sessions/.../rollout-*.jsonl`, …). The
runner never has to know the id at spawn time — which is why the old run-key /
`correlate.py` recovery layer is **gone**.

## How runs map to ledger sessions (no run-key, no window)

The runner records, per run, the **one** fact qvr can't infer: the **version tag**
it ran (the loop `qvr switch`ed to it first) — the candidate's identity — plus the
realpath **project** dir (its workspace). It records **no** timing and **no**
window: qvr stamps every session with the skill content version it ran. After
`discover`, the grader attributes qvr's sessions to a cohort by **skill identity**:

```
sv = session.skill_versions[]  where sv.skill == <skill>     # {version, commit, subtree_hash}
attribute session to the cohort with that sv.subtree_hash
&& session.working_directory == run.project                  # scope to this loop's workspace
```

`report.py` does exactly this for the cost axis — it folds the `subtree_hash` back
onto the runner's version tag (canonicalizing any ref↔commit label skew). It needs
no wall-clock window and no session id, so two versions can even run concurrently in
one project and stay separated by content identity. Quality, likewise, needs no
session id — it is `output` vs `expected` from `runs.jsonl`, keyed by the version tag.

## Attribution requires the skill be installed FOR each agent

This is the thing that actually gates the cross-agent matrix: an agent only
attributes a skill it loaded from *its own* catalog. If the inner skill is
installed for `claude` only, a `codex` run that merely reads the file off disk is
still attributed in 0.30.x (the SKILL span records `skill.activation=path`), but
to get it bucketed cleanly install it for every fan-out agent:

```
qvr add <skill> --target claude,codex     # install for every agent in --agents
qvr info <skill>                            # confirm a Targets row per agent
```

> **Cross-agent content hashes now AGREE.** In 0.30.x both claude (`activation=tool`)
> and codex (`activation=path`) resolve the same logical version to the **same**
> `subtree_hash`, so they bucket into one cohort in `compare`. (The old per-agent
> divergent-hash caveat is obsolete.) Cohort identity is still the version tag you
> switched to; `subtree_hash` is its stable fingerprint.

> **Version *label* is live-resolved in `spans`/`export` only (qvr 0.30.x).** There,
> `skill.version`/`ref`/`commit` reflect the **currently checked-out** worktree, not
> the version captured at run time, so a switched-away cohort's label reads empty
> while its `subtree_hash` stays correct — don't key on those two surfaces' labels.
> `audit sessions` (`skill_versions[]`) and `audit compare` (`ref` per cohort) are
> fixed: their labels are frozen + switch-invariant, trustworthy off-checkout.

## Determinism levers

- **Pin the model** (`--model`, or `QVR_LOOP_MODEL`) so a cohort is not silently
  graded across model upgrades. The runner records the model on every run row.
- **N ≥ 2** when the skill's output is nondeterministic; the grader averages.
- **Same prompt template** every round (it is one of the five frozen invariants).
- **Sequential `switch → run → discover` is tidiest** but no longer required for a
  clean join — `subtree_hash` separates versions even in one project, overlapping in
  time. A fresh project dir per loop still keeps the cost scope unambiguous.

## Cost without guessing

Token / turn / tool counts come from the ledger, not estimation:

```
qvr audit sessions --since 1h --limit 0 --output json   # per session: turns, tools, token totals, agent, skill_versions[]
```

`report.py` attributes those onto each cohort by the `subtree_hash` in each
session's `skill_versions[]` (no session id threaded through the runner) to get the
cost axis.
