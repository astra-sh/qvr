package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HashSubtreeFromDisk computes the canonical subtree digest by walking the
// actual on-disk contents under absRoot, NOT the git tree at HEAD. It mirrors
// HashSubtree's algorithm — `<git-mode>\0<rel path>\0<sha256(blob bytes)>\n`,
// sorted by path, sha256'd — so an untampered worktree produces the same
// digest as HashSubtree.
//
// This is the verifier-side hash. HashSubtree (git-tree based) is the
// installer-side hash. They agree on a fresh checkout and diverge whenever
// the working copy has been edited, replaced, or had files added/removed
// outside git's view. That divergence is what `qvr lock verify` reports as
// drift.
//
// File mode mapping matches go-git's filemode.FileMode.String() exactly —
// that's "%07o" formatting, e.g. "0100644" with a leading zero — because
// the mode string is concatenated verbatim into the digest input:
//
//	symlink (any os.ModeSymlink)        → "0120000", blob = link target bytes
//	executable bit set on owner/grp/oth → "0100755", blob = file bytes
//	otherwise (regular file)            → "0100644", blob = file bytes
//
// Directories are not hashed directly (their contents are walked). The same
// exclusion list (qvr.sig, .quiver-attestation.json) applies — those files
// are wrapper artifacts produced AFTER the digest, so they're skipped to
// keep the digest stable across signing.
func HashSubtreeFromDisk(absRoot string) (string, error) {
	info, err := os.Stat(absRoot)
	if err != nil {
		return "", fmt.Errorf("stat subtree root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("subtree root is not a directory: %s", absRoot)
	}

	type entry struct {
		mode string
		path string
		hash string
	}
	var entries []entry

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			return fmt.Errorf("relativize %s: %w", path, relErr)
		}
		// Canonicalise to forward-slash for cross-platform stability — git
		// stores paths with `/` regardless of host OS, and HashSubtree's
		// paths come from the git tree (already `/`-separated).
		rel = filepath.ToSlash(rel)
		if IsExcluded(rel) {
			return nil
		}

		fi, statErr := d.Info()
		if statErr != nil {
			return fmt.Errorf("stat %s: %w", path, statErr)
		}

		mode, blobHash, hashErr := diskBlobIdentity(path, fi)
		if hashErr != nil {
			return fmt.Errorf("hash %s: %w", rel, hashErr)
		}
		entries = append(entries, entry{mode: mode, path: rel, hash: blobHash})
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	if len(entries) == 0 {
		return "", errors.New("subtree contains no hashable files")
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
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// diskBlobIdentity returns the (git-style mode, hex sha256 of blob bytes)
// pair for a single filesystem entry. For symlinks the blob is the link
// target string (matching git's symlink representation). For regular files
// the executable bit determines 100755 vs 100644.
func diskBlobIdentity(absPath string, fi os.FileInfo) (string, string, error) {
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absPath)
		if err != nil {
			return "", "", err
		}
		// Symlink targets are stored as forward-slash too.
		target = strings.ReplaceAll(target, string(filepath.Separator), "/")
		sum := sha256.Sum256([]byte(target))
		return "0120000", hex.EncodeToString(sum[:]), nil
	}

	mode := "0100644"
	if fi.Mode().Perm()&0o111 != 0 {
		mode = "0100755"
	}

	f, err := os.Open(absPath)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", err
	}
	return mode, hex.EncodeToString(h.Sum(nil)), nil
}
