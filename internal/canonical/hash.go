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
	clean := strings.Trim(subpath, "/")
	if clean != "" {
		subTree, err = rootTree.Tree(clean)
		if err != nil {
			return nil, fmt.Errorf("locate subtree %q: %w", clean, err)
		}
	}

	type entry struct {
		mode string
		path string
		hash string
	}
	var entries []entry

	fileIter := subTree.Files()
	err = fileIter.ForEach(func(f *object.File) error {
		if IsExcluded(f.Name) {
			return nil
		}
		blobHash, herr := blobSHA256(f)
		if herr != nil {
			return fmt.Errorf("hash blob %s: %w", f.Name, herr)
		}
		entries = append(entries, entry{
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

	return &SubtreeIdentity{
		SubtreeHash: "sha256:" + hex.EncodeToString(h.Sum(nil)),
		TreeSHA:     subTree.Hash.String(),
		CommitSHA:   commit.Hash.String(),
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
