package derive

import (
	"regexp"
	"strings"
)

// worktreePathRe is the SINGLE grammar for a reference into qvr's immutable
// store — .quiver/worktrees/<registry>/<skill>/<sha7>/… — shared by skill
// DETECTION (scraping a load out of messy shell/JSON tool text, pathSkillRef)
// and IDENTITY (extracting registry+commit from an already-resolved load path,
// storeWorktreeIdentity). One pattern keeps those two readers from drifting:
// the multi-segment-registry bug existed precisely because each had its own
// copy and only one was fixed.
//
// Capturing groups:
//  1. the whole path token — the load path the tool actually referenced.
//  2. the registry WITH its trailing slash — one OR MORE path segments, matched
//     LAZILY (+?) so the LEFTMOST <skill>/<sha7> boundary wins. An <org>/<repo>
//     registry nests the store four levels deep (registry.WorktreePath), so a
//     single-segment matcher silently truncates it and the match fails; a GREEDY
//     match instead reaches past the real version pin to a coincidental
//     <name>/<7hex> directory inside the skill's own subtree. Lazy is the only
//     correct choice. Strip the trailing slash for the registry value.
//  3. the skill name (agentskills.io naming: [a-z0-9][a-z0-9-]{0,63}).
//  4. the 7-hex commit sha that pins the version.
//
// The token character classes exclude JSON and shell punctuation (quotes,
// braces, commas, backslashes) on top of whitespace so the pattern also matches
// inside compact serialized tool arguments ({"cmd":"sed … /SKILL.md"}) without
// swallowing the surrounding syntax into an unresolvable "path".
var worktreePathRe = regexp.MustCompile(`([^\s"'\\{}\[\],]*\.quiver/worktrees/((?:[^/\s"'\\{}\[\],]+/)+?)([a-z0-9][a-z0-9-]{0,63})/([0-9a-f]{7})(?:/[^\s"'\\{}\[\],]*)?)`)

// worktreeMatch is one parsed store-path reference: the full token the tool
// referenced plus the store coordinates it pins.
type worktreeMatch struct {
	token    string // the whole path token actually referenced
	registry string // <org>/<repo> or _local (trailing slash trimmed)
	skill    string
	sha      string // 7-hex commit
}

// worktreeMatchFrom builds a worktreeMatch from one regex submatch.
func worktreeMatchFrom(m []string) worktreeMatch {
	return worktreeMatch{
		token:    m[1],
		registry: strings.TrimSuffix(m[2], "/"),
		skill:    m[3],
		sha:      m[4],
	}
}

// parseWorktreePaths returns every store-path reference in text, in order — the
// detection reader, which scans messy command/argument text that may carry
// several tokens (and some unresolved ones the caller filters).
func parseWorktreePaths(text string) []worktreeMatch {
	ms := worktreePathRe.FindAllStringSubmatch(text, -1)
	out := make([]worktreeMatch, 0, len(ms))
	for _, m := range ms {
		out = append(out, worktreeMatchFrom(m))
	}
	return out
}

// parseWorktreePath returns the first store-path reference in text — the
// identity reader, which is handed a single already-resolved load path.
func parseWorktreePath(text string) (worktreeMatch, bool) {
	m := worktreePathRe.FindStringSubmatch(text)
	if m == nil {
		return worktreeMatch{}, false
	}
	return worktreeMatchFrom(m), true
}
