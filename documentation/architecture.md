# Architecture

## Overview

qvr is a CLI-native agent skills manager that uses Git repositories as the storage and versioning backbone. Skills are installed via symlinks into agent directories, enabling zero-overhead reads and bidirectional sync.

## Core Design: Bare Clone + Worktrees + Sparse Checkout + Symlinks

### Why This Design

The naive approach (clone repo → checkout branch → symlink) has three fatal flaws:

1. **Branch checkout is global** — switching `code-review` to `v2` switches ALL skills in the registry
2. **Entire registry downloaded** — 200-skill registry downloads all 200 even if you need 3
3. **No two-way sync** — `git pull` can clobber local agent modifications

The solution uses Git's own primitives:

- **Bare clones** for registries (no working tree, minimal disk, one fetch updates all refs)
- **Worktrees** for each installed skill (independent branch per skill, no conflicts)
- **Sparse checkout** per worktree (only the skill directory, not the whole repo)
- **Symlinks** into agent directories (zero-copy, instant reads, modifications flow both ways)

### Storage Layout

```
~/.quiver/
├── registries/
│   ├── acme-labs/                       # Org parent
│   │   └── agent-skills.git/              # Bare clone (objects + refs only)
│   └── example-org/
│       └── skills.git/
│
├── worktrees/                             # SHA-keyed, shared across projects
│   ├── acme-labs/
│   │   └── agent-skills/
│   │       └── code-review/
│   │           └── abc1234/               # Sparse: only skills/code-review/
│   └── example-org/
│       └── skills/
│           └── test-runner/
│               └── def5678/
│
├── config.yaml
├── qvr.lock                               # Global ambient lock (--global lane)
└── cache/
    └── index/                             # Registry index cache (per-registry
                                           #   skill catalog, TTL'd, rebuilt from
                                           #   the bare clone — not skill files)

<project>/
├── qvr.toml                               # Declarative intent (skills + default targets)
├── qvr.lock                               # Resolved proof — source of truth for agents
└── .claude/skills/<skill>  -->            symlink into ~/.quiver/worktrees/.../<sha7>/
```

### Intent vs. proof: `qvr.toml` and `qvr.lock`

A project's skill set lives in two committed files that move together. **`qvr.toml`**
is the hand-editable *intent* — a `[skills]` map of `<registry>/<skill>` → ref and
the project's `[project].default-targets` (the agents a bare `qvr add` installs
into). **`qvr.lock`** (schema **v5**) is the machine-generated *proof* — one fully
resolved entry per skill (source, resolved commit, subtree hash, scan decision,
commit author, targets).

Every mutating command (`qvr add`, `switch`, `remove`, `target`, …) writes through
to **both** files, so they never drift in normal use. When they do diverge (a
hand-edit, a merge), two explicit verbs reconcile them:

- **`qvr sync`** resolves toward the lock — the lock wins, because it's the
  reproducible truth. The lock is **self-sufficient**: `qvr sync` rebuilds the whole
  set from `qvr.lock` alone, so CI never needs `qvr.toml` and a lost `qvr.toml` is
  regenerated from the lock (routing policy and all).
- **`qvr lock --from-toml`** pushes `qvr.toml` edits into the lock — intent wins.

Single-skill repos live under the same `registries/` tree — `qvr registry add`
is the only entrypoint, so the indexer's job is to walk whatever's there
(one skill or many). Both `registries/` and `worktrees/` nest by
`<org>/<repo>` so the on-disk shape is uniform and a whole org can be wiped or
browsed at once.

### Source of truth vs. derived state

Only one thing on disk is authoritative; everything else is regenerable from it.
Keeping these distinct is why "cache" means two different things in Quiver — be
precise about which.

| Layer | Path | Role | Regenerable from |
|-------|------|------|------------------|
| **Bare clone** | `registries/<org>/<repo>.git/` | **Source of truth** — every object + ref. The network-expensive artifact. | upstream (re-clone) |
| **Registry index** | `cache/index/<name>.json` | Derived **catalog** of skills/versions a registry offers (names, descriptions, paths, refs). Powers discovery; holds no skill files. Persisted as a TTL **cache**. | the bare clone (re-index) |
| **Worktree store** | `worktrees/<org>/<repo>/<skill>/<sha7>/` | Derived, SHA-keyed **installs** — the materialized skill files the symlinks point at, shared across projects. | the lock + bare clone (`qvr sync`) |

Both derived layers are caches in the strict sense (rebuildable, disposable), which
is what lets `qvr cache clean` wipe them and lets `qvr remove` drop a shared worktree
without ref-counting — `qvr sync` rebuilds whatever a surviving project still needs.
The **registry index cache** answers "what skills exist?"; the **worktree store**
answers "which skills are installed, and what are their bytes?". `qvr cache prune`
GCs the latter (orphans that per-project `add`/`remove` structurally can't reclaim:
old SHAs left by `qvr switch`, or worktrees from a project deleted out-of-band).

### Data Flow

```
                REMOTE GIT REPO
                      │
           git fetch (bare clone)
                      │
              BARE CLONE (.git/)
                      │
         git worktree add --sparse
                      │
              WORKTREE (sparse)
              └── skills/code-review/
                      │
                   symlink
                      │
          ┌───────────┼───────────┐
          ▼           ▼           ▼
    .claude/skills  .agents/skills  .github/skills
    /code-review    /code-review    /code-review
   (claude)        (cursor/codex)   (copilot)
```

## Performance Model

### Hot Path (Every Agent Invocation)

An agent reading `.claude/skills/code-review/SKILL.md` (through the symlink):
- Follow symlink → `fs.ReadFile()` → return content
- **Zero git operations, zero network I/O** — qvr isn't even in the read path
- Latency: microseconds

### Warm Path (Local-Only)

`qvr status`, `qvr list`:
- Read lock file (single TOML file) or run `git status` per worktree
- No network I/O
- Latency: milliseconds

### Cold Path (Network)

`qvr registry update`, `qvr add` / `qvr sync`:
- `git fetch` on bare clone (one fetch = all refs)
- Create worktree + sparse checkout (disk I/O)
- Only when explicitly requested
- Latency: seconds (network-bound)

## Bidirectional Sync

### Upstream → local (`qvr switch` / `qvr pull`)

1. `git fetch` on bare repo (`qvr registry update`)
2. `qvr switch <skill> --tip` fast-forwards the worktree to the upstream tip
   (`qvr pull` is the alias); `qvr switch <skill> --latest` jumps to the newest
   semver tag
3. Symlinks unchanged (worktree path is SHA-keyed, repointed not moved)

### Local → upstream (`qvr edit` → `qvr publish`)

1. `qvr edit <skill>` ejects the immutable worktree into a real, editable dir
2. The agent (or you) modifies the skill in that dir — changes land in a git repo
3. `qvr publish <skill>` re-runs the lint + scan gate, then commits + pushes
   upstream (or to a fork with `--fork --migrate`); the lock entry is updated.
   qvr never auto-commits or auto-pushes on your behalf.

## Module Dependencies

```
cmd/ (Cobra commands)
  → internal/config/    (Viper config)
  → internal/skill/     (business logic: loader, linter, linker,
                         installer, syncer, publisher)
  → internal/registry/  (registry manager, indexer, registry-index TTL cache)
  → internal/output/    (formatting — text/JSON printer)

internal/skill/
  → internal/git/       (git operations — go-git + shell-out)
  → internal/model/     (data types — Skill, Registry, LockFile, …)

internal/registry/
  → internal/git/
  → internal/model/

pkg/skillspec/           (public, no internal deps)

# Shipped subsystems:
#   internal/security/                      (scan pipeline: injection, secrets,
#                                            unicode, permissions — gates qvr add)
#   internal/ops/ + internal/ops/<agent>/   (audit capture + SQLite store, per-agent
#                                            hook adapters: claudecode, cursor, codex,
#                                            opencode, copilot)
#   internal/ui/ + ui/                      (embedded React dashboard, qvr ui)
#   trust + provenance                      (per-registry commit-author policy and
#                                            lock-entry verification records)
```
