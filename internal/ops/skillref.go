package ops

import "strings"

// quiverReadVerbs are the `qvr <verb> <skill>` subcommands that read a skill
// by name without a path argument — the documented hot path. A shell-first
// agent (Codex) reaching a skill this way leaves no file path to match, so the
// skill name has to be mined from the command itself.
var quiverReadVerbs = map[string]bool{"read": true, "cat": true, "show": true}

// skillDirMarkers are path fragments that identify a skill-directory reference
// inside a shell command: the per-agent install dirs (".../skills/<name>",
// cursor's ".cursor/rules/<name>") and quiver's own worktree store. A token
// carrying one is a candidate path; the resolver still has the final say
// (it symlink-follows the token and matches it against installed skills), so
// an over-broad marker only costs a stat, never a false attribution.
var skillDirMarkers = []string{"skills/", "rules/", "worktrees/"}

// SkillRefFromCommand returns the skill named by a `qvr read <skill>` style
// hot-path call embedded in a shell command (also `qvr cat`/`qvr show`), or ""
// when the command is not one. This is how a shell-first agent's command_exec
// event surfaces an explicit skill reference — Codex never emits a discrete
// Skill tool-call, it just runs `qvr read <skill>`.
func SkillRefFromCommand(command string) string {
	fields := strings.Fields(command)
	for i := 0; i+2 < len(fields); i++ {
		if !isQuiverBinary(fields[i]) || !quiverReadVerbs[fields[i+1]] {
			continue
		}
		if name := strings.Trim(fields[i+2], "'\"`"); looksLikeSkillName(name) {
			return name
		}
	}
	return ""
}

// CommandSkillPaths scans a shell command for tokens that reference a skill
// directory by path — an agent install symlink (.codex/skills/<s>,
// .claude/skills/<s>, .cursor/rules/<s>, .github/copilot/skills/<s>,
// .agent/skills/<s>) or a quiver worktree path. Tokens are returned verbatim
// (still possibly relative); the resolver joins them against the event's
// working directory and follows the symlink to the worktree before matching.
func CommandSkillPaths(command string) []string {
	var out []string
	for _, tok := range strings.Fields(command) {
		tok = strings.Trim(tok, "'\"`")
		if tok == "" || !strings.ContainsRune(tok, '/') {
			continue
		}
		for _, m := range skillDirMarkers {
			if strings.Contains(tok, m) {
				out = append(out, tok)
				break
			}
		}
	}
	return out
}

// isQuiverBinary reports whether a command token invokes qvr (bare or an
// absolute/relative path to it).
func isQuiverBinary(tok string) bool {
	tok = strings.Trim(tok, "'\"`")
	return tok == "qvr" || strings.HasSuffix(tok, "/qvr")
}

// looksLikeSkillName applies the agentskills.io name rule (1-64 chars,
// lowercase alphanumeric + hyphens) so a flag or path isn't mistaken for a
// skill name when mining `qvr read <x>`.
func looksLikeSkillName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !lower && !digit && r != '-' {
			return false
		}
	}
	return s[0] != '-'
}

// SkillRefFromTool extracts the name of an explicitly-invoked skill from a
// tool-call's name + arguments, or "" when the call is not a skill
// invocation. It centralises the per-agent rules so every adapter that has
// a skill-as-tool-call signal is a one-liner and the rules are tested in
// one place.
//
// Coverage (see the audit plan): Claude Code and Codex invoke skills via a
// tool literally named "Skill"; OpenCode runs a skill as a tool named
// "skill"/"skills" (or a "skills_"-prefixed variant). Cursor and Copilot do
// not surface a discrete skill tool-call — they rely on the universal
// path-based signal in the resolver, so they never call this.
//
// toolName is matched case-insensitively; the skill name is read from the
// first present of a small set of argument keys.
func SkillRefFromTool(toolName string, args map[string]any) string {
	switch {
	case strings.EqualFold(toolName, "Skill"),
		strings.EqualFold(toolName, "skill"),
		strings.EqualFold(toolName, "skills"),
		strings.HasPrefix(strings.ToLower(toolName), "skills_"):
		return firstStringArg(args, "skill", "name", "command", "id")
	default:
		return ""
	}
}

// firstStringArg returns the first present non-empty string value among keys.
func firstStringArg(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
