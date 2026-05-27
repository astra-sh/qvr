package canonical

// ExcludedFromSubtree lists paths that are NOT part of a skill's canonical
// subtree digest. These are wrapper artifacts produced *after* the digest
// is computed (the signature can't be inside the thing it signs) or that
// live outside the subtree (the lockfile). Computing the subtree hash with
// these included would create a circular dependency when signing.
//
// Paths are relative to the skill subtree root, not the worktree root.
var ExcludedFromSubtree = map[string]bool{
	"qvr.sig":                  true,
	".quiver-attestation.json": true,
}

// IsExcluded reports whether a path (relative to the subtree root) should
// be skipped during canonical-hash computation.
func IsExcluded(relPath string) bool {
	return ExcludedFromSubtree[relPath]
}
