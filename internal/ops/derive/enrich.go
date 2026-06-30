package derive

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	reg "github.com/astra-sh/qvr/internal/registry"
)

// EnrichSkillIdentity promotes full skill identity onto SKILL/TOOL spans whose
// recorded load path PROVES which artifact the agent ran, by resolving the
// span's skill.name against the calling project's qvr.lock (falling back to
// the user-global lock) and checking the load path for containment in that
// entry's managed worktree. The lockfile pins each skill to registry + commit
// + ref + subtreeHash, so two registries that both ship "code-review", or two
// pinned versions of the same skill across projects, become distinguishable
// in logs/spans/UI/OTLP without scraping raw command strings (issue #146).
//
// It is intentionally a SEPARATE pass from DeriveSession, not folded into the
// derivers: a Deriver must be PURE (same rows in → same spans out), and the
// lockfile is external state not carried in the rows. Enrichment is applied at
// the persistence/display boundaries (capture + `qvr audit spans`), where
// reflecting the current pin is what callers want.
//
// Identity is proof-gated: the presence of skill.version IS the verification
// signal (version present ⇔ proven; no separate boolean). Attributes added
// when — and only when — the span's skill.load_path resolves into the lock
// entry's worktree:
//
//	skill.registry     — the registry the skill came from (e.g. "raks")
//	skill.version      — the requested ref (branch/tag), or the short pinned
//	                     SHA when the entry has no ref — always set on proof,
//	                     so "has skill.version" is the one verification test
//	skill.commit       — the entry's pinned git SHA, abbreviated to git's
//	                     7-char short form so every proof source records it
//	                     identically (a store-path proof carries only the short
//	                     SHA; see applyIdentity)
//	skill.source       — the fetch coordinate (git URL or local path)
//	skill.subtree_hash — canonical content hash of the installed subtree
//	skill.canonical    — upstream skill name when installed under an alias
//
// Everything else keeps skill.name (and skill.load_path, when recorded) as
// evidence and gains NO identity fields: a span with no load path (an agent
// that never records what it read), a path that resolves outside the entry's
// worktree (a global eject shadowing the project install, a copy from another
// registry, a drifted sha), or a name absent from every lock (built-in agent
// skills, skills installed by another tool). qvr never attests a
// registry+commit+subtree_hash the agent didn't provably run (#149). The
// unproven name→lock pin is still useful context, but it is a DISPLAY-TIME
// join (UI/CLI resolve name→lock at render and label it as the current pin),
// never persisted onto spans as identity.
//
// By construction this means a skill loaded from a path that is NOT a managed
// worktree carries no commit/version: an edit-mode or project-loaded skill
// read straight from its source tree (.claude/skills/<name> or
// .github/skills/<name>/SKILL.md, not a ~/.quiver worktree) is provably some
// content, but not a PINNED version, so its identity (registry/commit/version)
// stays unknown. The evolution loop still distinguishes its before/after
// revisions, though: skill.content_hash is the digest of the verbatim body the
// run loaded (stamped by the deriver, finalized by stampContentHash), so two
// revisions of an in-place skill bucket apart by content without either being
// committed to a worktree.
//
// snap optionally carries the session's ingest-time identity snapshot
// (skill name → frozen entry). Symlink-origin evidence (an agent-dir path
// like claude's base-directory line) can only be resolved against the
// filesystem AS IT IS NOW, so a rederive after a version move would rewrite
// history; the snapshot — harvested at first ingest, when resolution still
// matched run time — wins for that evidence class. Transcript-pinned store
// paths (the sha is in the recorded path itself) stay path-truth and ignore
// the snapshot. Pass nil when no snapshot exists (first ingest, display-time
// re-derivation of unpersisted rows).
func EnrichSkillIdentity(spans []Span, rows []*ops.RawTrace, snap map[string]*model.LockEntry) {
	if len(spans) == 0 {
		return
	}
	wd := workingDir(rows)
	r := newLockResolver(wd)
	for i := range spans {
		attrs := spans[i].Attributes
		name, ok := attrs["skill.name"].(string)
		if !ok || name == "" {
			continue
		}
		loadPath, _ := attrs["skill.load_path"].(string)
		if loadPath == "" {
			continue // no evidence — nothing to prove against
		}
		proveIdentity(attrs, name, loadPath, wd, r, snap)
		stampContentHash(attrs, loadPath)
	}
}

// EscalateUnresolvedIdentity upgrades a SKILL span that ran a known version but
// was resolved OFF-CHECKOUT (so it carries only run-immutable evidence — a bare
// commit, or a body-digest content_hash — without the ref/subtree the version
// has elsewhere) to the full identity qvr already proved for that same evidence
// in another session. byCommit handles codex's transcript-pinned commit;
// byContent handles claude's symlink-recorded body digest (no commit in the
// bytes). Both are lock-INDEPENDENT, so once ANY session proves a version, every
// run of it labels uniformly regardless of the current checkout. A span already
// carrying a subtree (full) is left untouched.
func EscalateUnresolvedIdentity(spans []Span, byCommit, byContent func(key string) *model.LockEntry) {
	for i := range spans {
		a := spans[i].Attributes
		if a == nil {
			continue
		}
		if sub, _ := a["skill.subtree_hash"].(string); sub != "" {
			continue // already full
		}
		e := escalateLookup(a, byCommit, byContent)
		if e == nil {
			continue
		}
		applyIdentity(a, e)
		if e.SubtreeHash != "" {
			// Coordinate by the proven subtree, matching a transcript-pinned load
			// (see stampContentHash), so escalated runs cohort with the rest.
			a[SkillContentHashKey] = e.SubtreeHash
		}
	}
}

// escalateLookup tries the run-immutable keys in precedence: the exact commit
// (codex), then the body-digest content_hash (claude). Returns nil when neither
// resolves.
func escalateLookup(a map[string]any, byCommit, byContent func(string) *model.LockEntry) *model.LockEntry {
	if commit, _ := a["skill.commit"].(string); commit != "" && byCommit != nil {
		if e := byCommit(commit); e != nil {
			return e
		}
	}
	if ch, _ := a[SkillContentHashKey].(string); ch != "" && byContent != nil {
		return byContent(ch)
	}
	return nil
}

// proveIdentity runs the proof-gated identity resolution for one span: a
// transcript-pinned store path, then the ingest-time snapshot, then a
// derive-time symlink resolve, then lock-entry worktree containment. It writes
// the verified identity fields onto attrs when — and only when — one of those
// proofs passes; otherwise the span keeps just its name/load_path evidence.
func proveIdentity(attrs map[string]any, name, loadPath, wd string, r *lockResolver, snap map[string]*model.LockEntry) {
	// Transcript-pinned: the RECORDED path (before any symlink resolution) is
	// already inside qvr's immutable store — registry, skill, and short sha are
	// in the bytes the agent wrote, so this is proof ACROSS time (codex/openclaw
	// record resolved paths). When the current lock pin matches, the richer lock
	// fields ride along.
	abs := absLoadPath(loadPath, wd)
	if pathReg, _, sha := storeWorktreeIdentity(abs); sha != "" {
		applyPathIdentity(attrs, r.lookup(name), pathReg, sha)
		return
	}

	// Symlink-origin evidence from here down: prefer the ingest-time snapshot
	// when one exists — it froze the proof while the symlink still pointed where
	// it did at run time.
	if e := snap[name]; e != nil {
		applyIdentity(attrs, e)
		stampSnapshotContentHash(attrs, e)
		return
	}

	// No snapshot: resolve the symlink now (derive-time truth — correct for
	// near-live ingest, the case that then gets snapshotted). A symlink is
	// resolved AS IT IS NOW, so if the install was switched between the run and
	// this ingest, it points at the wrong version. Guard against that with the
	// run-immutable evidence: when the captured body digest does NOT match the
	// resolved worktree's body, the symlink moved — withhold identity rather than
	// stamp (and freeze) the wrong version. The run keeps its body-digest
	// content_hash, so it stays attributed to what it actually ran and never
	// conflates with a different version. (Matching is the common case — near-live
	// ingest — and there it stamps the proven identity as before.)
	real := resolveLoadPath(loadPath, wd)
	if pathReg, _, sha := storeWorktreeIdentity(real); sha != "" {
		if !resolvedBodyMatches(attrs, real) {
			return // switched-after-run: keep the run-immutable body digest, prove nothing
		}
		applyPathIdentity(attrs, r.lookup(name), pathReg, sha)
		return
	}
	// Non-store evidence: assert identity only when the loaded file resolves
	// into the lock entry's worktree; otherwise the loaded copy is provably not
	// the locked one. No body guard here: this branch is reached only for a
	// recorded path that is NOT a store-worktree path (the store cases above own
	// the switched-after-run guard), and a by-path load's captured body may be a
	// partial read that legitimately differs from the full file.
	if entry := r.lookup(name); entry != nil && loadedFromEntryWorktree(loadPath, wd, entry) {
		applyIdentity(attrs, entry)
	}
}

// skillFrontmatterRe matches a leading YAML frontmatter block in a SKILL.md.
var skillFrontmatterRe = regexp.MustCompile(`(?s)^---\n.*?\n---\n`)

// resolvedBodyMatches reports whether the worktree the symlink currently
// resolves to actually holds the body this run loaded — the switch detector for
// symlink-recorded loads. It compares the span's run-immutable body digest
// (skill.content_hash, what the agent loaded) against a freshly computed digest
// of the resolved SKILL.md body, normalized identically (frontmatter stripped,
// since the harness injects the body without it; see the claude deriver). A
// missing captured digest or an unreadable file returns true — we can only
// DISCONFIRM with positive contrary evidence, never withhold on absence (that
// would drop legitimate near-live ingests). Reading here verifies identity; it
// never sets the coordinate from disk (a mismatch keeps the captured body
// digest), so the run stays bucketed by what it ran.
func resolvedBodyMatches(attrs map[string]any, resolvedPath string) bool {
	want, _ := attrs[SkillContentHashKey].(string)
	if want == "" {
		return true // no captured body to check against — don't withhold
	}
	p := resolvedPath
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		p = filepath.Join(p, "SKILL.md")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return true // can't read the worktree — don't disconfirm on absence
	}
	body := skillFrontmatterRe.ReplaceAllString(string(data), "")
	return runContentHash(body) == want
}

// stampContentHash finalizes skill.content_hash, the evolution loop's content
// coordinate, WITHOUT ever reading the skill from disk — the bug that made
// cohorts "confidently wrong": resolving the recorded load path at discover time
// re-binds it to whatever the working tree points at NOW, so any run discovered
// after a switch/edit/publish was mis-attributed to the current version. The
// coordinate is fixed by the captured bytes instead, in two run-immutable forms:
//
//	transcript-pinned — the proven version sha (skill.commit) appears as a
//	  directory segment of the RECORDED load path (…/<sha7>/…), i.e. the agent
//	  wrote a store-worktree path, so the version lives in the captured bytes.
//	  The proven full-subtree attestation (skill.subtree_hash) is the coordinate;
//	  when identity didn't resolve to a subtree (the skill is no longer in any
//	  lock), the commit carries it downstream, so a partial-read run-time body
//	  digest must NOT out-vote it — any deriver-stamped one is cleared.
//	run-time body — a symlink-recorded load (claude) or an eject carries no sha
//	  in its recorded path; the sha, if any, was only recovered by resolving that
//	  symlink at discover and is NOT run-immutable. The digest the deriver took of
//	  the verbatim body in the transcript (stampRunContentHash) is the run-time
//	  signal, and it stays the coordinate.
//
// A load with neither leaves content_hash unset — the honest unknown, which
// downstream coalesces to the proven commit when there is one.
func stampContentHash(attrs map[string]any, loadPath string) {
	commit, _ := attrs["skill.commit"].(string)
	if commit == "" || !strings.Contains(loadPath, "/"+commit+"/") {
		return // not transcript-pinned: keep the deriver's run-time body digest
	}
	if sub, ok := attrs["skill.subtree_hash"].(string); ok && sub != "" {
		attrs[SkillContentHashKey] = sub // proven artifact: coordinate == attestation
		return
	}
	delete(attrs, SkillContentHashKey)
}

// stampSnapshotContentHash supplies the evolution-loop coordinate from a
// snapshot-proven identity ONLY as a last resort: when the run captured no
// verbatim body for the deriver to digest. The body digest, when present, is
// strictly more trustworthy and MUST win — it is fixed by the exact bytes the
// run loaded, whereas the snapshot's subtree_hash is frozen at FIRST INGEST,
// which can post-date a version switch (the session whose run we are deriving
// may have been discovered only after the symlink moved). Overriding a body
// digest with the snapshot subtree would re-bind a run to the wrong version on
// re-derive — the exact disk-at-discover failure the content coordinate exists
// to avoid. So a snapshot subtree coordinates a run only when there is no body
// digest to prefer (a by-path load with no captured result); otherwise it is
// left to identity (commit/subtree_hash) and never touches the coordinate.
func stampSnapshotContentHash(attrs map[string]any, e *model.LockEntry) {
	if _, ok := attrs[SkillContentHashKey]; ok {
		return // a run-immutable body digest already coordinates this run
	}
	if e.SubtreeHash != "" {
		attrs[SkillContentHashKey] = e.SubtreeHash
	}
}

// applyPathIdentity stamps identity proven by a store path: the full lock
// fields when the current pin matches the path's sha, else the minimal
// path-derived identity (version = short sha — still verified).
func applyPathIdentity(attrs map[string]any, entry *model.LockEntry, pathReg, sha string) {
	if entry != nil && strings.HasPrefix(entry.Commit, sha) {
		applyIdentity(attrs, entry)
		return
	}
	attrs["skill.registry"] = pathReg
	attrs["skill.commit"] = sha
	attrs["skill.version"] = sha // version presence = verified
}

// HarvestVerifiedIdentities collects the proven identity per skill from
// enriched spans — the rows persistDerivation freezes as the session's
// snapshot on first ingest. Only verified spans (skill.version present)
// contribute; the snapshot never records guesses.
func HarvestVerifiedIdentities(spans []Span) map[string]*model.LockEntry {
	out := map[string]*model.LockEntry{}
	str := func(m map[string]any, k string) string {
		s, _ := m[k].(string)
		return s
	}
	for i := range spans {
		attrs := spans[i].Attributes
		name := str(attrs, "skill.name")
		if name == "" || str(attrs, "skill.version") == "" {
			continue
		}
		if _, seen := out[name]; seen {
			continue
		}
		out[name] = &model.LockEntry{
			Name:        name,
			Registry:    str(attrs, "skill.registry"),
			Ref:         str(attrs, "skill.version"),
			Commit:      str(attrs, "skill.commit"),
			SubtreeHash: str(attrs, "skill.subtree_hash"),
			Source:      str(attrs, "skill.source"),
			Canonical:   str(attrs, "skill.canonical"),
		}
	}
	return out
}

// absLoadPath absolutizes a recorded load path against the session's working
// directory WITHOUT resolving symlinks — the recorded bytes, locatable. A
// leading "~/" is expanded first: some agents record home-relative paths (e.g.
// ~/.quiver/worktrees/...) that filepath.Join would otherwise leave literal.
func absLoadPath(loadPath, workingDir string) string {
	loadPath = expandHome(loadPath)
	if !filepath.IsAbs(loadPath) && workingDir != "" {
		return filepath.Join(workingDir, loadPath)
	}
	return loadPath
}

// expandHome replaces a leading "~/" (or a bare "~") with the user's home
// directory, best-effort: an unresolvable home leaves the path untouched.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	return p
}

// storeWorktreeIdentity extracts (registry, skill, sha7) from a resolved store
// path, or zero values when the path is not in the store. It reads the shared
// worktreePathRe grammar (worktree_path.go) — the same parser skill detection
// uses — so identity and detection can never disagree on where the registry
// ends and the skill begins (the multi-segment-registry bug that lazy matching
// fixes existed because each side once had its own copy of this pattern).
func storeWorktreeIdentity(path string) (registry, skill, sha string) {
	if wm, ok := parseWorktreePath(path); ok {
		return wm.registry, wm.skill, wm.sha
	}
	return "", "", ""
}

// resolveLoadPath absolutizes a recorded load path against the session's
// working directory and resolves symlinks best-effort.
func resolveLoadPath(loadPath, workingDir string) string {
	abs := expandHome(loadPath)
	if !filepath.IsAbs(abs) && workingDir != "" {
		abs = filepath.Join(workingDir, abs)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// loadedFromEntryWorktree reports whether loadPath — the file the agent actually
// referenced — resolves into the managed worktree that entry pins. loadPath may
// be an absolute worktree path or a relative agent-dir symlink; symlinks are
// resolved best-effort and the result is checked for containment under the
// entry's worktree root. A miss (eject, shadowing copy, drifted sha, or a path
// that no longer resolves) returns false, so identity is asserted only when the
// bytes the agent loaded are provably the locked ones (#149).
func loadedFromEntryWorktree(loadPath, workingDir string, e *model.LockEntry) bool {
	if loadPath == "" || e.Commit == "" {
		return false
	}
	abs := expandHome(loadPath)
	if !filepath.IsAbs(abs) && workingDir != "" {
		abs = filepath.Join(workingDir, abs)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	root := reg.WorktreePath(e.Registry, e.Name, reg.ShortSHA(e.Commit))
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// applyIdentity writes the non-empty identity fields of entry onto attrs.
// skill.version is ALWAYS set (falling back to the short pinned SHA when the
// entry has no ref) because its presence is the verification signal — callers
// only invoke this after the load-path proof has passed.
func applyIdentity(attrs map[string]any, e *model.LockEntry) {
	set := func(k, v string) {
		if v != "" {
			attrs[k] = v
		}
	}
	set("skill.registry", e.Registry)
	version := e.Ref
	if version == "" {
		version = reg.ShortSHA(e.Commit)
	}
	set("skill.version", version)
	// Canonicalize to the 7-char short SHA. A store-path proof can only ever
	// recover the short SHA (the worktree dir is named with it), so emitting
	// the lock's full/abbreviated commit here would record the SAME commit as
	// two different strings depending on which proof path ran — splitting one
	// version into two for any equality/dedup (e.g. SkillVersionRollup's GROUP
	// BY commit). The short form is the one representation every proof source
	// agrees on, and the lineage walk already resolves short SHAs (gogit.go).
	set("skill.commit", reg.ShortSHA(e.Commit))
	set("skill.source", e.Source)
	set("skill.subtree_hash", e.SubtreeHash)
	set("skill.canonical", e.Canonical)
}

// workingDir returns the first non-empty working directory across a session's
// rows. All rows in a session share the same cwd (the hook payload's), so the
// first one is authoritative; "" when no row recorded one.
func workingDir(rows []*ops.RawTrace) string {
	for _, r := range rows {
		if r.WorkingDirectory != "" {
			return r.WorkingDirectory
		}
	}
	return ""
}

// lockResolver maps a skill name to its lock entry, consulting the project
// lock first and the user-global lock as a fallback. Both locks are read once,
// lazily, and cached for the lifetime of one enrichment pass.
type lockResolver struct {
	workingDir string

	project       *model.LockFile
	projectLoaded bool
	global        *model.LockFile
	globalLoaded  bool
}

func newLockResolver(workingDir string) *lockResolver {
	return &lockResolver{workingDir: workingDir}
}

// lookup returns the lock entry for a skill name, or nil if neither lock has
// it. A failed/missing lock read is treated as "no entry" (ReadLockFile
// returns an empty lock for a missing file), so enrichment degrades to a no-op
// rather than failing derivation.
func (r *lockResolver) lookup(name string) *model.LockEntry {
	if r.workingDir != "" {
		if !r.projectLoaded {
			r.project = readLock(model.DefaultLockPath(r.workingDir, "", false))
			r.projectLoaded = true
		}
		if e := getEntry(r.project, name); e != nil {
			return e
		}
	}
	if !r.globalLoaded {
		r.global = readLock(model.DefaultLockPath("", config.Dir(), true))
		r.globalLoaded = true
	}
	return getEntry(r.global, name)
}

func readLock(path string) *model.LockFile {
	l, err := model.ReadLockFile(path)
	if err != nil {
		return nil
	}
	return l
}

func getEntry(l *model.LockFile, name string) *model.LockEntry {
	if l == nil {
		return nil
	}
	e, err := l.Get(name)
	if err != nil {
		return nil
	}
	return e
}
