# Audit & Tracing

> **Experimental and opt-in.** The `qvr audit` subsystem is disabled by default.
> Its command surface, storage schema, and output shapes may change — pin your
> `qvr` version if you script against them.

You can't optimize what you can't measure. `qvr audit` records what your agents
actually did — every turn, tool call, and command — **attributed to the skill
that was active** — so you can evaluate and improve skills on evidence rather than
guesswork. Everything stays local: capture lands in a SQLite database at
`~/.quiver/skillops.db`, and nothing is sent anywhere.

## Two layers: raw traces and derived spans

- **Raw traces** — the agent's own transcript lines and hook payloads, captured
  **verbatim**. This is the lossless source of truth (`qvr audit export`,
  `qvr audit sessions show`).
- **Derived spans** — Turn / Tool / Skill spans *projected* from the raw traces
  using the OpenTelemetry [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/),
  shown by `qvr audit logs`. A `skill.*` attribute family tags which skill each
  span belongs to. A *deriver* must exist for the agent; without one the agent is
  raw-only and the derived views stay empty (the `DERIVES` column in `status`
  reports this per agent).

Because spans are derived, an improved deriver can re-run over old captures
without re-capturing.

## Workflow

### 1. Enable capture and wire agent hooks

`enable` sets `ops.enabled` in config and creates the database. `install-hooks`
adds a hook to each detected agent's native config that pipes events into qvr;
the original config is backed up first under `$QUIVER_HOME/backups/<agent>/<ts>/`.
Hook adapters ship for **claudecode, cursor, codex, copilot, and opencode**.

```bash
qvr audit enable
qvr audit install-hooks                  # wire every detected agent
qvr audit install-hooks --agent codex    # wire a single agent
qvr audit install-hooks --dry-run        # show planned changes, write nothing
```

Nothing is captured until **both** steps run — `enable` alone creates the DB but
no events flow; hooks alone fire but `disable` keeps them from recording.

### 2. Confirm it's recording

```bash
qvr audit status
```

Read the columns: detected, hooks installed/valid, `DERIVES` (raw-only vs. derived
views available), `RECORDED` (events), `SESSIONS` (runs they group into),
`ERRORS` (non-zero means events reach qvr but fail to record), and last-event time.

### 3. Use your agent normally

With hooks wired and capture enabled, run the agent as usual. Tool calls, file
ops, and commands are recorded and attributed to the active skill.

### 4. Query activity

```bash
qvr audit sessions                                  # newest-first, with row counts
qvr audit sessions --agent claude --since 24h
qvr audit sessions show <session-id>                # one session's verbatim raw lines

qvr audit logs                                      # derived spans (default 50)
qvr audit logs --kind SKILL                         # only skill spans (or LLM / TOOL)
qvr audit logs --session <session-id> --limit 0     # everything for one session
```

### 5. Export for external analysis

`export` streams matching raw trace rows as JSONL (one object per line) —
suitable for archival, analysis, or replay, and OTLP-ready for any OpenTelemetry
consumer (Jaeger, Tempo, Honeycomb, an OTel Collector):

```bash
qvr audit export > traces.jsonl
qvr audit export --session <session-id> -o session.jsonl
qvr audit export --source hook_payload              # or: transcript
```

### 6. Turn it off / unwire

```bash
qvr audit disable                        # stop recording (hooks may still fire silently)
qvr audit uninstall-hooks                # remove hooks / restore from backup
qvr audit uninstall-hooks --agent codex
```

## The dashboard

The read-only `qvr ui` dashboard (embedded in the binary, served at
`http://127.0.0.1:7878`) visualizes recorded sessions alongside a skill's files,
targets, scan results, version history, and provenance — drilling from a registry
down to a single skill.

```bash
qvr ui
```

## Notes

- **Local only.** The database lives under `~/.quiver/`; nothing leaves the machine.
- **`DERIVES=no` ⇒ empty derived views.** `logs` / spans / UI timeline will be
  blank for raw-only agents even though raw traces land — use `sessions show` or
  `export` to read the raw data.
- **`ERRORS > 0` in status** means hook payloads are arriving but failing to
  ingest — investigate before trusting the data.
- Low-level plumbing verbs (`gc`, `ingest`, `raw`, `rederive`, `spans`) exist for
  maintenance and re-derivation but are hidden — prefer the verbs above.

See the [trace-skill-activity](../skills/trace-skill-activity/SKILL.md) skill for
a task-oriented walkthrough, and [config-reference.md](config-reference.md) for
the `ops.enabled` gate.
