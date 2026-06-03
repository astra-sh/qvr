// Package canonical provides deterministic hashing for qvr skill subtrees
// and canonical JSON serialization for verification artifacts. Every
// signature, attestation, and `qvr lock verify` call ultimately resolves
// to a SubtreeHash produced here — its output is the load-bearing
// identity for the supply-chain pipeline.
package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// SubtreeIdentity bundles the three identity fields lifted out of a single
// `gogit.PlainOpen` + tree walk. The lockfile stores all three so a future
// verifier can replay against either the working tree or a fresh clone.
type SubtreeIdentity struct {
	SubtreeHash string // "sha256:<hex>" — canonical digest over the subtree
	TreeSHA     string // git tree SHA for the subtree
	CommitSHA   string // HEAD commit of the worktree
}

// HashSubtree computes the canonical subtree digest of the skill at
// `subpath` inside the git worktree at `worktreePath`. The digest is
// deterministic across machines: it hashes git-stored blob bytes (not
// the working-tree files), normalising line endings and respecting git's
// content addressing.
//
// Algorithm (frozen — every later phase signs against this output):
//  1. Open the worktree at HEAD.
//  2. Resolve HEAD → commit → root tree → subtree at `subpath`.
//  3. Walk all blobs recursively, filtering paths in ExcludedFromSubtree.
//  4. For each blob, compute `<octal mode>\0<rel path>\0<sha256(blob bytes)>\n`.
//  5. Sort entries lexicographically by relative path.
//  6. Hash the concatenation with sha256 and return "sha256:<hex>".
//
// `subpath` is the skill's location inside the repo (e.g. "skills/my-skill").
// Use "" to hash the entire worktree.
func HashSubtree(worktreePath, subpath string) (*SubtreeIdentity, error) {
	repo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("open worktree: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD: %w", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("load commit %s: %w", head.Hash(), err)
	}
	return hashSubtreeFromCommit(commit, subpath)
}

// HashSubtreeAtCommit is HashSubtree pinned to an explicit commit instead of
// HEAD. It hashes the subtree straight from git objects, so it works against a
// bare clone (no working tree, HEAD on the registry default branch) for any
// commit reachable in that repo. `qvr lock` uses it to re-pin and re-hash an
// entry without checking out a worktree — the digest is identical to what a
// checkout of the same commit would produce, since both walk the same tree.
func HashSubtreeAtCommit(repoPath, commitHash, subpath string) (*SubtreeIdentity, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return nil, fmt.Errorf("load commit %s: %w", commitHash, err)
	}
	return hashSubtreeFromCommit(commit, subpath)
}

// hashSubtreeFromCommit is the shared tail of HashSubtree / HashSubtreeAtCommit:
// resolve the subtree at `subpath` within the commit's tree and hash it with
// the frozen algorithm documented on HashSubtree.
func hashSubtreeFromCommit(commit *object.Commit, subpath string) (*SubtreeIdentity, error) {
	rootTree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("load tree: %w", err)
	}

	subTree := rootTree
	// `path: "."` denotes the repo root — the same target as an empty subpath.
	// strings.Trim only strips '/', so "." survives it; without this guard a
	// root-layout skill walks into rootTree.Tree(".") and dies with "locate
	// subtree \".\": directory not found" (issues #151/#154).
	clean := strings.Trim(subpath, "/")
	if clean == "." {
		clean = ""
	}
	if clean != "" {
		subTree, err = rootTree.Tree(clean)
		if err != nil {
			return nil, fmt.Errorf("locate subtree %q: %w", clean, err)
		}
	}

	var entries []hashEntry
	fileIter := subTree.Files()
	err = fileIter.ForEach(func(f *object.File) error {
		if IsExcluded(f.Name) {
			return nil
		}
		blobHash, herr := blobSHA256(f)
		if herr != nil {
			return fmt.Errorf("hash blob %s: %w", f.Name, herr)
		}
		entries = append(entries, hashEntry{
			mode: f.Mode.String(),
			path: f.Name,
			hash: blobHash,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("subtree contains no hashable files")
	}

	return &SubtreeIdentity{
		SubtreeHash: digestEntries(entries),
		TreeSHA:     subTree.Hash.String(),
		CommitSHA:   commit.Hash.String(),
	}, nil
}

// hashEntry is one (git mode, subtree-relative path, blob sha256) tuple fed
// into the frozen digest. Shared by the subtree and scoped hashers so both
// produce byte-identical input for the same content.
type hashEntry struct {
	mode string
	path string
	hash string
}

// digestEntries sorts the entries lexicographically by path and folds them
// into the frozen `<mode>\0<path>\0<hash>\n` digest documented on HashSubtree.
func digestEntries(entries []hashEntry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, e := range entries {
		_, _ = h.Write([]byte(e.mode))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.hash))
		_, _ = h.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// HashScoped is HashSubtree for a root-layout skill that shares its repo with
// sibling skills: instead of one contiguous subtree it hashes a SET of
// repo-root entries (SKILL.md + the recognized content dirs, per
// model.SkillScopePaths). The resulting paths are repo-root-relative, exactly
// matching what HashSubtreeFromDisk produces over a worktree sparse-checked-out
// to the same scope — so install-side and verify-side digests agree.
//
// An empty scope means "no narrowing" → the whole worktree, identical to
// HashSubtree(worktreePath, "").
func HashScoped(worktreePath string, scope []string) (*SubtreeIdentity, error) {
	if len(scope) == 0 {
		return HashSubtree(worktreePath, "")
	}
	repo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("open worktree: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD: %w", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("load commit %s: %w", head.Hash(), err)
	}
	return hashScopedFromCommit(commit, scope)
}

// HashScopedAtCommit is HashScoped pinned to an explicit commit, the scoped
// analogue of HashSubtreeAtCommit. Used by `qvr lock` re-pin against a bare
// clone with no worktree.
func HashScopedAtCommit(repoPath, commitHash string, scope []string) (*SubtreeIdentity, error) {
	if len(scope) == 0 {
		return HashSubtreeAtCommit(repoPath, commitHash, "")
	}
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return nil, fmt.Errorf("load commit %s: %w", commitHash, err)
	}
	return hashScopedFromCommit(commit, scope)
}

// hashScopedFromCommit hashes the union of the given repo-root scope entries
// within commit. Each scope entry may be a file (e.g. "SKILL.md") or a
// directory (e.g. "references"); a directory contributes all of its blobs
// recursively, with repo-root-relative paths. Scope entries that don't exist
// in the tree (e.g. an absent "assets") are silently skipped, mirroring a
// sparse checkout that simply materializes nothing for them.
func hashScopedFromCommit(commit *object.Commit, scope []string) (*SubtreeIdentity, error) {
	rootTree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("load tree: %w", err)
	}

	var entries []hashEntry
	for _, raw := range scope {
		p := strings.Trim(raw, "/")
		if p == "" || p == "." {
			continue
		}
		// A scope entry is either a blob (SKILL.md) or a subtree (references/).
		if f, ferr := rootTree.File(p); ferr == nil {
			if IsExcluded(p) {
				continue
			}
			blobHash, herr := blobSHA256(f)
			if herr != nil {
				return nil, fmt.Errorf("hash blob %s: %w", p, herr)
			}
			entries = append(entries, hashEntry{mode: f.Mode.String(), path: p, hash: blobHash})
			continue
		}
		sub, terr := rootTree.Tree(p)
		if terr != nil {
			continue // absent scope entry — nothing to checkout, nothing to hash
		}
		walkErr := sub.Files().ForEach(func(f *object.File) error {
			full := p + "/" + f.Name
			if IsExcluded(full) {
				return nil
			}
			blobHash, herr := blobSHA256(f)
			if herr != nil {
				return fmt.Errorf("hash blob %s: %w", full, herr)
			}
			entries = append(entries, hashEntry{mode: f.Mode.String(), path: full, hash: blobHash})
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("subtree contains no hashable files")
	}

	return &SubtreeIdentity{
		SubtreeHash: digestEntries(entries),
		// No single git tree object represents a scoped union; the root tree is
		// the closest native anchor. TreeSHA is informational (info/provenance
		// display) and never used for drift comparison.
		TreeSHA:   rootTree.Hash.String(),
		CommitSHA: commit.Hash.String(),
	}, nil
}

func blobSHA256(f *object.File) (string, error) {
	r, err := f.Reader()
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
