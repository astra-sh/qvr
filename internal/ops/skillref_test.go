package ops

import "testing"

func TestSkillRefFromTool(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"claude/codex Skill via skill key", "Skill", map[string]any{"skill": "code-review"}, "code-review"},
		{"command key fallback", "Skill", map[string]any{"command": "x-copywriter"}, "x-copywriter"},
		{"name key fallback", "Skill", map[string]any{"name": "deep-research"}, "deep-research"},
		{"opencode lowercase skill", "skill", map[string]any{"skill": "verify"}, "verify"},
		{"opencode skills plural", "skills", map[string]any{"name": "verify"}, "verify"},
		{"opencode skills_ prefixed", "skills_run", map[string]any{"id": "run"}, "run"},
		{"ordinary Bash is not a skill", "Bash", map[string]any{"command": "ls"}, ""},
		{"ordinary Read is not a skill", "Read", map[string]any{"file_path": "/x"}, ""},
		{"skill tool but no name arg", "Skill", map[string]any{"unrelated": "v"}, ""},
		{"empty tool", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SkillRefFromTool(tc.tool, tc.args); got != tc.want {
				t.Errorf("SkillRefFromTool(%q, %v) = %q want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}
