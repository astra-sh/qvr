# qvr audit cheatsheet (experimental — pin --output json)

The audit subsystem is the ledger. It captures, attributes, stores your scores,
and shows cohorts. It never grades. Everything below is one round's worth of
calls.

## Lifecycle

```
qvr audit enable                       # once per machine; capture on
qvr audit status                       # per-agent capture state
qvr audit discover --agent claude,codex   # back-fill the sessions a cohort just produced
```

`discover` walks agents' own on-disk session stores (Claude Code's
`~/.claude/projects`, Codex rollout files, …). It is incremental and safe to
re-run; sessions that used no skill are counted but not stored.

## Attribution & cohorts

```
qvr audit sessions --since 1h --limit 0 --output json   # newest-first: agent_name, model, turns, tools, skills, tokens_in/out, working_directory, skill_versions[]
qvr audit logs --skill <skill> --output json            # derived spans (LLM/TOOL/SKILL)
qvr audit logs --skill <skill> --status failure         # status = "did it error", NOT a quality grade
qvr audit compare <skill> --output json                 # bucket runs by subtree_hash; SCORE = your annotate mean
qvr audit spans --session <id> --output json            # the clean Turn/LLM/Skill span tree for one session
```

- **Cohort key = `subtree_hash`** (`contentHash` in `compare`). It is the only
  capture-time-stable identity: both agents of a version share one, and it never
  moves when you switch/edit. Each `SKILL` span also carries `skill.registry`,
  `skill.commit`, `skill.activation` (`tool` vs `path`), `skill.load_path`.
- **The version LABEL is live-resolved in `spans`/`export` ONLY** (qvr 0.30.x):
  there, `ref`/`commit`/`skill.version` reflect the currently checked-out worktree,
  so only the checked-out cohort shows a populated `ref` and every other reads
  `ref=""`/`commit=""`/`skill.version=None` (its `subtree_hash` stays correct). Don't
  key the loop on **those two surfaces'** labels. **`audit sessions` and `audit
  compare` are fixed** — each cohort carries a frozen, switch-invariant label
  (`sessions[].skill_versions[]`; `compare`'s per-cohort `ref`), trustworthy
  off-checkout. `subtree_hash` is still the identity and the join key.
- `compare` with no `--version` compares the two most-recent cohorts; pass
  `--version <prefix>` twice to pin a pair.
- `qvr audit sessions` carries, per session, `agent_name` + `working_directory` +
  `skill_versions[]` (`{skill, version, commit, subtree_hash}` per skill it ran) —
  enough to attribute a session to a cohort by **skill identity** (the `subtree_hash`,
  scoped to the project), with no wall-clock window and no live-resolved label.

## Scoring (the only place your numbers enter qvr)

`annotate` keys a grade by the **agent-native session id** + the `--skill` it
judges (so a multi-skill session's grade isn't double-counted). You get the
session ids from qvr after `discover` (`qvr audit sessions`/`compare`) — the runner
no longer carries them. Annotating is **optional**: it only feeds `compare`'s SCORE
column; `report.py` computes quality from the version-stamped score rows, not from
annotations.

```
# one run
qvr audit annotate <session-id> --skill <skill> --metric exact  --score 1.0 --grader exact
qvr audit annotate <session-id> --skill <skill> --metric rubric --score 0.8 --grader llm-judge

# a whole cohort from a grader file (one JSON object per line):
#   {"session":"…","agent":"claude","skill":"…","metric":"exact","score":1.0,"grader":"exact"}
qvr audit annotate --from scores/<tag>.exact.jsonl
qvr audit annotate --from -            # or read JSONL from stdin
```

Use distinct `--metric` names per dimension (`exact`, `rubric`) so they fold into
`compare` as separate per-cohort means. `--agent` defaults to `claude`; set it
for other agents' sessions. `--strict` refuses a grade whose already-discovered
run never loaded `--skill`.

## Reading why a run failed

```
qvr audit export --session <id> --source transcript   # verbatim lines, JSONL
qvr audit sessions show <id>                            # same, human-readable
```

This is what the rubric judge reads, and what you cluster to find the dominant
failure pattern.
