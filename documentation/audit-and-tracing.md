# Audit & Tracing

> **Experimental and opt-in.** The `qvr audit` subsystem is disabled by default.
> Its command surface, storage schema, and output shapes may change — pin your
> `qvr` version if you script against them.

You can't optimize what you can't measure. `qvr audit` records what your agents
actually did — every turn, tool call, and command — **attributed to the skill
that was active** — so you can evaluate and improve skills on evidence rather than
guesswork. Everything stays local: capture lands in a SQLite database at
`~/.quiver/skillops.db`, and nothing is sent anywhere.

Agents are the capture infrastructure: each one already persists its own session
history on disk (Claude Code's `~/.claude/projects`, Codex's rollout files, …).
`qvr audit discover` reads those native stores directly — **no agent
configuration is ever touched**, and months of existing history back-fill on the
first scan.

## Two layers: raw traces and a derived projection

- **Raw traces** — the agent's own transcript lines, captured **verbatim**. This
  is the lossless source of truth (`qvr audit export`, `qvr audit sessions show`).
- **Derived projection** — the unified per-session model (title, model, turn /
  tool counts, skills used) plus Turn / Tool / Skill spans, *projected* from the
  raw traces using the OpenTelemetry
  [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/),
  shown by `qvr audit sessions` and `qvr audit logs`. A `skill.*` attribute
  family tags which skill each span belongs to, resolved against `qvr.lock`
  (`skill.verified` marks identity proven by the loaded path).

Because the projection is derived, an improved deriver re-runs over old captures
without re-capturing (`qvr audit rederive`). Derivers ship for **claude, codex,
copilot, cursor, gemini, droid, pi, hermes, and openclaw**; an agent without a
deriver is listed but inert until one ships.

## Workflow

### 1. Enable capture and discover your history

```bash
qvr audit enable                       # opt in; creates the database
qvr audit discover                     # scan every agent's session store
qvr audit discover --agent claude      # scan a single agent
qvr audit discover --since 90d         # bound the back-fill window
qvr audit discover --dry-run           # report what would be scanned
```

Scans are incremental and idempotent: a stat ledger remembers every file seen,
so re-running over an unchanged store costs almost nothing. Sessions that
provably used **no** skill are counted but not stored (qvr keeps
skill-attributed evidence, not generic transcripts); pass `--keep-all` to import
everything — including sessions a prior plain `discover` already skipped, which
`--keep-all` re-evaluates even when the file is otherwise unchanged.

Re-run `discover` whenever you want fresh sessions picked up — or just open
the dashboard: `qvr ui` scans on launch and keeps rescanning in the background
while it runs, so new sessions appear live (`--no-discover` turns the scans
off; the discover button forces one on demand).

### 2. Confirm what's recorded

```bash
qvr audit status
```

Read the columns: `DERIVES` (whether qvr can project this agent's format),
`RECORDED` (raw rows), `SESSIONS`, and last-event time.

### 3. Query activity

```bash
qvr audit sessions                                  # newest-first, titled, with skills
qvr audit sessions --agent claude --since 24h
qvr audit sessions show <session-id>                # one session's verbatim raw lines

qvr audit logs                                      # derived spans (default 50)
qvr audit logs --kind SKILL                         # only skill spans (or LLM / TOOL)
qvr audit logs --session <session-id> --limit 0     # everything for one session
```

### 4. Compare versions, and grade quality

`compare` buckets a skill's runs by the **content version** that produced them —
the run-immutable content hash captured from each trace, so a run stays with the
version it actually ran even after a `qvr edit` or switch — and prints the cohorts
side by side with their run-status breakdown. That is the before/after evidence
for evolving a skill.

```bash
qvr audit compare <skill>                         # the two newest content versions
qvr audit compare <skill> --version <a> --version <b>   # pin an explicit pair
qvr audit compare <skill> --by-agent              # one row per agent: the {version × agent} matrix
```

Each cohort carries both halves of the frontier: **SCORE** (your graded
pass-rate) and **TOKENS** (in/out over the cohort's sessions — session-attributed
exposure, not exclusive cost; n/a when the agent reported no usage). `--by-agent`
splits each version into one row per agent, because the best version can differ by
agent; the **PARETO** column marks the cells on each agent's (quality↑, cost↓)
frontier with `*` — a reading aid, never a winner verdict.

Run status (success / failure / blocked) is an observed fact, **not** a quality
grade. To add a quality dimension, bring your own grader (exact / regex / an
LLM-judge — anything) and record its verdict against the run with `annotate`,
keyed by the agent-native session id the runner held and the `--skill` the grade
judges. The grade asserts "this skill's run scored X", not "this session scored
X": a session can load several skills, so `--skill` scopes the grade to the one
it judged and keeps a multi-skill session's grade from double-counting across the
others. qvr stores the number; it never computes it. The score then folds into
`compare` as a per-cohort `SCORE` (a pass-rate), attributed to whichever version
the run loaded:

```bash
qvr audit annotate <session-id> --skill triage --metric accuracy --score 1.0 --grader exact
qvr audit annotate --from scores.jsonl            # batch; each line carries its own "skill"
qvr audit compare triage --metric accuracy        # SCORE column shows the pass-rate
```

The write is blind — you can grade immediately after a run, before the session is
even discovered, and the score is picked up on the next scan. Add `--strict` to
refuse a grade whose already-discovered run never loaded the named skill (without
it, that case is warned and recorded).

### 5. Export for external analysis

`export` streams the **derived span tree** as JSONL (one span per line) — the
same clean Turn / model / tool / skill spans the UI shows. Spans are the
shareable, normalized view, so downstream tools consume them rather than each
agent's private transcript format:

```bash
qvr audit export > spans.jsonl
qvr audit export --session <session-id> -o session.jsonl
qvr audit export --session <session-id> --otlp > otlp.json   # OTLP-ready (Jaeger, Tempo, Honeycomb, an OTel Collector)
qvr audit export --session <session-id> --raw > transcript.jsonl   # the verbatim native transcript + hook payloads
```

Use `--raw` only when you need the agent's byte-for-byte transcript (archival or
replay); the default span export is what other tools should ingest.

### 6. Turn it off

```bash
qvr audit disable                        # stop recording; the database stays
```

## The dashboard

The `qvr ui` dashboard (embedded in the binary, served at
`http://127.0.0.1:7878`) visualizes recorded sessions and activity analytics —
sessions over time split by agent and skill usage, the by-skill breakdown, the
skill report card and dead-weight view — alongside a skill's files, targets,
scan results, version history, and provenance.

```bash
qvr ui                   # live-scans session stores while running (--no-discover to skip)
```

## Notes

- **Local only.** The database lives under `~/.quiver/`; nothing leaves the machine.
- **No agent configuration is modified.** Discovery only reads the session files
  agents already write. (Earlier qvr versions installed hooks into agent
  configs; that mechanism is gone — if you ran `install-hooks` on an old
  version, remove the `qvr _hook` entries from your agents' hook configs.)
- **`DERIVES=no` ⇒ not scanned.** Discovery only ingests agents whose format qvr
  can derive, so the skill-retention gate stays provable.
- Low-level plumbing verbs (`gc`, `ingest`, `raw`, `rederive`, `spans`) exist for
  maintenance and re-derivation but are hidden — prefer the verbs above.

See the [trace-skill-activity](../skills/trace-skill-activity/SKILL.md) skill for
a task-oriented walkthrough, and [config-reference.md](config-reference.md) for
the `ops.enabled` gate.
