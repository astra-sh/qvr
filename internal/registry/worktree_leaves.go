package registry

import (
	"io/fs"
	"os"
	"path/filepath"
)

// WorktreeLeaves returns every on-disk worktree ROOT under the worktrees root —
// the set `qvr cache prune` compares against the reachable set
// (registry.Reachable) to find orphans.
//
// A worktree root lives at the deterministic `<registry>/<skill>/<sha>` depth
// that WorktreePath builds, where `<sha>` is the install commit (ShortSHA → 7+
// hex). Two markers identify a root, config-independently, in a single walk:
//
//   - a `.git` entry (dir or file) — a legacy git worktree; or
//   - a directory NAME that is a commit hash (looksLikeCommitDir) — the `<sha>`
//     segment of WorktreePath, present whether the install is a real git
//     worktree or a worktree-free content dir (#204, no `.git`). The materialized
//     skill content lives BELOW this dir (often at the repo subpath, e.g.
//     `<sha>/skills/<name>/SKILL.md`), so the root is NOT marked by SKILL.md —
//     keying on SKILL.md returned the nested content dir, which never matched the
//     reachable root and made every live worktree look orphaned (data-loss
//     regression #231). The hash-named segment is at the correct root depth.
//
// Discovery is deliberately NOT scoped to the configured registries: a registry
// removed from config keeps its worktrees on disk (Manager.Remove deletes only
// the bare clone), so a config-scoped enumeration leaked them (`cache prune`
// reported "0 worktree(s)" — issue #4). The walk SkipDirs once a root is found,
// so it never descends into content that may itself nest hash-like dirs.
// Reachability (applied by the caller, with prefix-safe matching) still gates
// deletion, so a mis-identified level can only leak, never delete a live tree.
func WorktreeLeaves() []string {
	root := WorktreesRoot()
	if _, err := os.Stat(root); err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	var leaves []string
	add := func(p string) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		leaves = append(leaves, p)
	}

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil
		}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
			add(path)
			return filepath.SkipDir
		}
		if looksLikeCommitDir(d.Name()) {
			add(path)
			return filepath.SkipDir
		}
		return nil
	})

	return leaves
}

// looksLikeCommitDir reports whether a directory name is a commit-hash segment
// as produced by ShortSHA (7 hex) or a full 40-hex SHA — the `<sha>` level of a
// worktree path. Registry and skill path segments are names, not bare lowercase
// hex of this length, so the shallowest hash-named dir (top-down) is the worktree
// root. The reachability check is prefix-safe, so even a pathological all-hex
// registry/skill name only leaks (gets protected as an ancestor of a reachable
// root) rather than causing a wrong deletion.
func looksLikeCommitDir(name string) bool {
	if len(name) < 7 {
		return false
	}
	for _, c := range name {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
