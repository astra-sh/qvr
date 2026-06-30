package derive

import "testing"

// TestPathSkillRef_TokenBoundaries pins the path-token extraction across the
// text shapes derivers feed it: shell commands, compact JSON arguments, and
// bare paths. The JSON cases are the regression: a whitespace-only token class
// swallowed the surrounding JSON syntax and produced unresolvable load paths
// (observed in real session stores, 2026-06-11).
func TestPathSkillRef_TokenBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantName string
		wantLoad bool
		wantPath string
	}{
		{
			name:     "shell command",
			text:     `sed -n '1,40p' .codex/skills/code-review/SKILL.md`,
			wantName: "code-review",
			wantLoad: true,
			wantPath: ".codex/skills/code-review/SKILL.md",
		},
		{
			name:     "compact JSON file_path argument",
			text:     `{"file_path":"/home/u/p/.claude/skills/qa-demo/SKILL.md"}`,
			wantName: "qa-demo",
			wantLoad: true,
			wantPath: "/home/u/p/.claude/skills/qa-demo/SKILL.md",
		},
		{
			name:     "escaped JSON inside a string value",
			text:     `{"cmd":"cat /u/.quiver/worktrees/raks/code-review/94e539b/skills/code-review/SKILL.md\",..."}`,
			wantName: "code-review",
			wantLoad: true,
			wantPath: "/u/.quiver/worktrees/raks/code-review/94e539b/skills/code-review/SKILL.md",
		},
		{
			name:     "bare directory path (no SKILL.md)",
			text:     `/home/u/p/.claude/skills/frontend-design`,
			wantName: "frontend-design",
			wantLoad: false,
			wantPath: "/home/u/p/.claude/skills/frontend-design",
		},
		{
			name:     "no skill reference",
			text:     `ls -la ./src`,
			wantName: "",
		},
		{
			// An unexpanded shell variable is not a materialized skill: the
			// path token carries a "$", so it must not invent a skill (real
			// claude stores, 2026-06-24: `mkdir -p $REG/skills/clean-skill`).
			name:     "unexpanded shell variable is not a skill path",
			text:     `mkdir -p $REG/skills/clean-skill`,
			wantName: "",
		},
		{
			name:     "glob in path is not a skill load",
			text:     `cat /tmp/build-*/skills/foo/SKILL.md`,
			wantName: "",
		},
		{
			// Agents resolve the agent-dir symlink and read the store
			// directly; a local install's subtree has no skills/<name>/
			// segment, so the store layout is its own signal (observed in
			// real codex rollouts, 2026-06-11).
			name:     "resolved local-install worktree path",
			text:     `{"cmd": "sed -n '1,220p' /Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/SKILL.md", "workdir": "/tmp"}`,
			wantName: "qvr-probe",
			wantLoad: true,
			wantPath: "/Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/SKILL.md",
		},
		{
			name:     "worktree supporting-file read is not a load",
			text:     `sh /Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/scripts/stamp.sh`,
			wantName: "qvr-probe",
			wantLoad: false,
			wantPath: "/Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/scripts/stamp.sh",
		},
		{
			// A registry install nests the store <org>/<repo>/<skill>/<sha7>/
			// (registry.WorktreePath), so the <registry> component spans two path
			// segments. A single-segment matcher reads <repo> as the skill and
			// <skill> as the sha and finds nothing — silently dropping every
			// non-local install read via its resolved worktree path (real codex
			// rollout, 2026-06-27: a skillops-sql SKILL.md sed read).
			name:     "resolved org/repo registry worktree path",
			text:     `{"cmd": "sed -n '1,220p' /Users/u/.quiver/worktrees/qvr-skillops/skillops-sql/skillops-sql/5013dc8/SKILL.md"}`,
			wantName: "skillops-sql",
			wantLoad: true,
			wantPath: "/Users/u/.quiver/worktrees/qvr-skillops/skillops-sql/skillops-sql/5013dc8/SKILL.md",
		},
		{
			name:     "org/repo registry worktree supporting-file read is not a load",
			text:     `sh /Users/u/.quiver/worktrees/qvr-skillops/skillops-sql/skillops-sql/5013dc8/scripts/run.sh`,
			wantName: "skillops-sql",
			wantLoad: false,
			wantPath: "/Users/u/.quiver/worktrees/qvr-skillops/skillops-sql/skillops-sql/5013dc8/scripts/run.sh",
		},
		{
			// A supporting file living at a coincidental <name>/<7hex> path INSIDE
			// the skill's own subtree must not out-vote the real version pin: the
			// LEFTMOST registry/skill/sha boundary wins (lazy registry match). A
			// greedy matcher would mis-read the skill as "references" here.
			name:     "nested <name>/<7hex> subtree dir does not shadow the real skill",
			text:     `cat /Users/u/.quiver/worktrees/qvr-skillops/skillops-sql/skillops-sql/5013dc8/references/0f1e2d3/q.sql`,
			wantName: "skillops-sql",
			wantLoad: false,
			wantPath: "/Users/u/.quiver/worktrees/qvr-skillops/skillops-sql/skillops-sql/5013dc8/references/0f1e2d3/q.sql",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, isLoad, path := pathSkillRef(tt.text, nil)
			if name != tt.wantName {
				t.Fatalf("name = %q, want %q", name, tt.wantName)
			}
			if name == "" {
				return
			}
			if isLoad != tt.wantLoad {
				t.Errorf("isLoad = %v, want %v", isLoad, tt.wantLoad)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}
