package skill

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

// CheckGitProvenance derives optional, git-native provenance for an install:
// whether the resolved ref carries a verifiable Git signature, and who signed
// it. It prefers a signed annotated tag (the requested ref) and falls back to
// the commit-level signature. Returns nil when nothing could be checked
// (e.g. git couldn't read the repo) so the caller records no misleading
// "none". A returned ProvenanceRef with SignatureStatus == invalid is the
// only outcome an install should treat as fatal.
//
// repoPath is the bare/working repo to verify against; ref is the requested
// version label; commit is the resolved SHA used for the commit-level
// fallback.
func CheckGitProvenance(repoPath, ref, commit string) *model.ProvenanceRef {
	ctx := context.Background()
	// Prefer the requested ref as a signed annotated tag.
	if status, signer, err := git.VerifyTagSignature(ctx, repoPath, ref); err == nil {
		if status == git.SigVerified || status == git.SigInvalid {
			return &model.ProvenanceRef{
				Provider:        "git",
				Tag:             ref,
				SignatureStatus: status,
				Signer:          signer,
			}
		}
	}
	// Fall back to the commit-level signature.
	target := commit
	if target == "" {
		target = ref
	}
	status, signer, err := git.VerifyCommitSignature(ctx, repoPath, target)
	if err != nil {
		return nil
	}
	return &model.ProvenanceRef{
		Provider:        "git",
		SignatureStatus: status,
		Signer:          signer,
	}
}

// ComputeSubtreeHash returns the canonical content hash of a skill subtree
// rooted at worktreePath/subpath. This is the load-bearing integrity value
// stored on LockEntry.SubtreeHash — drift detection compares this to a
// fresh recomputation.
func ComputeSubtreeHash(worktreePath, subpath string) (string, error) {
	id, err := canonical.HashSubtree(worktreePath, subpath)
	if err != nil {
		return "", fmt.Errorf("canonical hash: %w", err)
	}
	return id.SubtreeHash, nil
}

// ComputeSubtreeIdentity returns the full canonical identity of a skill
// subtree — the load-bearing SubtreeHash plus the native git TreeSHA and
// HEAD commit. Installer uses this to record both LockEntry.SubtreeHash
// (the integrity anchor) and LockEntry.TreeOID (the informational
// git-native identity) from a single tree walk.
func ComputeSubtreeIdentity(worktreePath, subpath string) (*canonical.SubtreeIdentity, error) {
	id, err := canonical.HashSubtree(worktreePath, subpath)
	if err != nil {
		return nil, fmt.Errorf("canonical hash: %w", err)
	}
	return id, nil
}

// EntryWorktreePath returns the on-disk worktree path for a lock entry by
// re-deriving it from its registry / name / install-commit via
// registry.WorktreePath. Link installs return their Source (the absolute
// local path).
//
// The path is keyed by entry.InstallCommit (shortened to 7 hex) — pinned
// at install time so Pull / Switch advancing entry.Commit doesn't move the
// directory out from under the existing symlinks. Entries written before
// this field existed fall back to entry.Commit so legacy v5 installs keep
// resolving.
//
// Aliased entries (installed via `qvr add <skill> --as <alias>`) have
// entry.Name == alias but the worktree on disk is keyed by the canonical
// registry-side name — the install path builds finalPath with the
// canonical name (installer.go: registry.WorktreePath(reg, name, sha)),
// so we mirror that here by preferring entry.Canonical when set. Without
// this every read-side caller (info, status, diff, edit, doctor) goes
// looking at .../reg/<alias>/<sha> while the real dir lives at
// .../reg/<canonical>/<sha>. Issue #102.
func EntryWorktreePath(entry *model.LockEntry) string {
	if entry == nil {
		return ""
	}
	if entry.IsLink() {
		return entry.Source
	}
	key := entry.InstallCommit
	if key == "" {
		key = entry.Commit
	}
	if key == "" {
		return ""
	}
	name := entry.Name
	if entry.Canonical != "" {
		name = entry.Canonical
	}
	return registry.WorktreePath(entry.Registry, name, registry.ShortSHA(key))
}

// RefreshSubtreeHash recomputes entry.SubtreeHash from the on-disk worktree.
// Called after Pull / Switch / Upgrade so the lock stays aligned with the
// git state. Link installs are skipped — they have no upstream subtree to
// re-hash from this code path.
func RefreshSubtreeHash(entry *model.LockEntry) error {
	if entry == nil || entry.IsLink() {
		return nil
	}
	worktreePath := EntryWorktreePath(entry)
	hash, err := ComputeSubtreeHash(worktreePath, entry.Path)
	if err != nil {
		return err
	}
	entry.SubtreeHash = hash
	return nil
}

// RepairResult captures what RepairSubtreeHashFromDisk changed about an
// entry. Empty OldSubtreeHash means the entry had no recorded hash before
// repair. NewSubtreeHash is empty only on failure.
//
// OldCommit / NewCommit are populated when --repair healed entry.Commit
// to the worktree HEAD (issue #73). Empty when no commit-field drift was
// detected. This is the in-band fix for a tampered or stale commit field;
// callers can render a before/after diff from the two values.
type RepairResult struct {
	OldSubtreeHash string
	NewSubtreeHash string
	OldCommit      string
	NewCommit      string
	Failed         bool
	Error          string
}

// RepairSubtreeHashFromDisk rewrites entry.SubtreeHash using the on-disk
// worktree (working copy, including uncommitted edits) as the source of
// truth. This is the in-band recovery path for the `qvr edit` workflow
// where the user knowingly intends their disk state to be what's recorded.
//
// Also re-seals entry.Commit to the worktree HEAD when the recorded value
// has drifted (issue #73 — without this, a tampered `commit` field could
// only be fixed by manual lockfile editing).
//
// Unlike RefreshSubtreeHash, which uses HashSubtree (git tree at HEAD) and
// is therefore blind to uncommitted edits, this uses HashSubtreeFromDisk.
//
// projectRoot is consulted only for mode:edit entries with a relative
// EditPath; callers without one in scope may pass "".
func RepairSubtreeHashFromDisk(entry *model.LockEntry, projectRoot string) RepairResult {
	res := RepairResult{}
	if entry == nil || entry.IsLink() {
		res.Failed = true
		res.Error = "link install — no subtree to repair"
		return res
	}
	res.OldSubtreeHash = entry.SubtreeHash

	subtreeDir := ResolveSkillRepoPath(entry, projectRoot)
	if entry.IsEdit() {
		// mode:edit entries hash the edit dir directly — entry.Path doesn't
		// apply because the edit dir IS the skill, not a subpath of one.
	} else {
		subtreeDir = filepath.Join(EntryWorktreePath(entry), entry.Path)
	}
	diskHash, err := canonical.HashSubtreeFromDisk(subtreeDir)
	if err != nil {
		res.Failed = true
		res.Error = err.Error()
		return res
	}
	entry.SubtreeHash = diskHash
	res.NewSubtreeHash = diskHash

	// Re-seal entry.Commit to the worktree HEAD when it has drifted. Failure
	// to read HEAD is non-fatal — a degraded repo shouldn't block repair of
	// the subtree hash, which is still load-bearing.
	if head, hErr := ResolveEntryHeadCommit(entry, projectRoot); hErr == nil && head != "" && head != entry.Commit {
		res.OldCommit = entry.Commit
		res.NewCommit = head
		entry.Commit = head
	}
	return res
}
