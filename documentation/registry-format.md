# Registry Format

A registry is a Git repository that contains skills. qvr clones registries as bare repos and uses worktrees for installed skills.

## Repository Structure

```
acme-skills/
├── qvr.toml                         # Registry manifest: [registry] table (optional)
├── skills/                          # Skills directory
│   ├── code-review/
│   │   └── SKILL.md
│   ├── deploy-helper/
│   │   ├── SKILL.md
│   │   ├── scripts/deploy.sh
│   │   └── references/GUIDE.md
│   └── test-runner/
│       └── SKILL.md
└── README.md                        # Optional: human-readable docs
```

## Registry Manifest

The registry manifest is the `[registry]` table in the repo's top-level
`qvr.toml`. It scopes skill discovery when qvr indexes the repo and is
**optional** — without one, qvr discovers every directory containing a
`SKILL.md` across the whole tree (minus `testdata/` and `fixtures/`, which are
always excluded).

`qvr.toml` is dual-intent: `[project]`/`[skills]` declare what the repo
*consumes*, `[registry]` declares how the repo is indexed when *published* as
a registry. qvr-managed rewrites of `qvr.toml` round-trip a hand-authored
`[registry]` table losslessly. A `qvr.toml` that fails to parse never
silently mis-scopes: the parse failure is surfaced as a skip and discovery
falls back to whole-tree.

```toml
[registry]
name = "acme-skills"
skills-dir = "skills"                # where discovery looks (default: skills)
ignore = ["skills/experimental-*"]   # path.Match globs, repo-relative dirs
```

### Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | No | Registry identifier |
| `skills-dir` | No | Repo-relative directory skills live under (default: `skills`; the repo root itself stays eligible so a single-skill root-layout repo can carry a manifest) |
| `ignore` | No | `path.Match` globs evaluated against each candidate skill directory; matches are skipped |

Unknown keys are accepted and ignored — parsing is deliberately loose because
manifests are read from untrusted remote HEADs. Directories excluded by the
manifest are reported as informational skips (the SKIPPED column in
`qvr registry list`), never silently dropped.

### Artifact hierarchy

qvr has exactly two files, needed in strictly decreasing order:

1. **`qvr.lock`** — the only source of portability and reproducibility.
   `qvr sync` reproduces the full install from the lock alone; CI never needs
   anything else.
2. **`qvr.toml`** — optional. The human intent layer (`[project]`,
   `[skills]`); an absent `qvr.toml` is a no-op everywhere.
3. **`[registry]` within `qvr.toml`** — optional within optional. Read only
   when *other people's* qvr indexes your repo as a registry; a consumer-only
   `qvr.toml` is inert for indexing.

## Versioning Model

Skills are versioned via **Git branches and tags**:

- **Branches** = development versions (`main`, `v2`, `experimental`)
- **Tags** = release versions (`v1.0.0`, `v1.1.0`, `v2.0.0`)
- **`main`** (or default branch) = latest stable

```bash
# Install latest (default branch)
qvr add code-review

# Install specific branch
qvr add code-review@v2

# Install specific tag
qvr add code-review@v1.0.0
```

**Resolution order**: exact tag → exact branch → error.

## Team membership and access control

Quiver doesn't define a team file. Membership, permissions, and review gating are owned by your git host — GitHub Teams + branch protection + CODEOWNERS, or the equivalent on your platform. See [team-workflows.md](guides/team-workflows.md) for the recommended layout.

## Standalone Skill Repos

A skill can also live in its own Git repository (not a registry):

```
my-skill/
├── SKILL.md
├── scripts/
└── references/
```

Added via `qvr add <repo-url>` instead of `qvr registry add`.

## How qvr Uses Registries

1. **Add**: `qvr registry add git@github.com:acme/skills.git`
   - Name is inferred as `<org>/<repo>` (here `acme/skills`); override with
     `--name` only when two repos collide
   - Bare clone to `~/.quiver/registries/acme/skills.git/`

2. **Index**: qvr reads git tree objects from the bare repo to discover skills
   - No checkout needed — reads blob objects directly
   - The resulting registry index (skill catalog) is cached at `~/.quiver/cache/index/acme.json`

3. **Install**: Creates a git worktree with sparse checkout for the specific skill
   - Each skill independently versioned

4. **Update**: `git fetch` on bare clone updates all refs
   - Then rebase only affected worktrees

## Creating a Registry

```bash
# 1. Create a new Git repo
mkdir my-skills && cd my-skills
git init

# 2. Declare the registry manifest (optional — whole-tree discovery without it)
qvr init
cat >> qvr.toml << 'EOF'

[registry]
name = "my-skills"
skills-dir = "skills"
EOF

# 3. Create a skill (free-standing dir inside skills/)
mkdir -p skills && (cd skills && qvr create my-first-skill --standalone)

# 4. Commit and push
git add . && git commit -m "Initial skills"
git remote add origin git@github.com:me/skills.git
git push -u origin main

# 5. Others can now add your registry (name inferred as me/skills)
qvr registry add git@github.com:me/skills.git
```
