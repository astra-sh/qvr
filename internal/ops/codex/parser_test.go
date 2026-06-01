package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/ops"
)

func TestParseHookEvent_SkillRef(t *testing.T) {
	e, err := parseHookEvent("PreToolUse",
		[]byte(`{"session_id":"s1","cwd":"/repo","tool_name":"Skill","tool_input":{"skill":"verify"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.SkillRef != "verify" {
		t.Errorf("SkillRef=%q want verify", e.SkillRef)
	}
	e2, err := parseHookEvent("PreToolUse",
		[]byte(`{"session_id":"s1","tool_name":"Read","tool_input":{"file_path":"/x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e2.SkillRef != "" {
		t.Errorf("SkillRef=%q want empty for Read", e2.SkillRef)
	}
}

func setupCodexHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QVR_HOME", filepath.Join(home, ".quiver"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_SESSION_ID", "")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	return home
}

func TestParseHookEvent(t *testing.T) {
	t.Setenv("CODEX_SESSION_ID", "")
	tests := []struct {
		name       string
		hookType   string
		raw        string
		wantAction ops.ActionType
		wantTool   string
		wantResult ops.ResultStatus
	}{
		{"SessionStart", "SessionStart", `{"session_id":"s1","cwd":"/repo"}`, ops.ActionSessionStart, "", ops.ResultSuccess},
		{"PreToolUse Write", "PreToolUse", `{"session_id":"s1","tool_name":"Write","tool_input":{"file_path":"/repo/a.go","content":"x"}}`, ops.ActionFileWrite, "Write", ops.ResultSuccess},
		{"PostToolUse Bash", "PostToolUse", `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"output":"ok"}}`, ops.ActionCommandExec, "Bash", ops.ResultSuccess},
		{"PreToolUse apply_patch update", "PreToolUse", `{"session_id":"s1","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Update File: pkg/a.go\n@@\n-old\n+new\n*** End Patch"}}`, ops.ActionFileWrite, "apply_patch", ops.ResultSuccess},
		{"PostToolUse apply_patch delete", "PostToolUse", `{"session_id":"s1","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Delete File: pkg/gone.go\n*** End Patch"},"tool_response":{"output":"done"}}`, ops.ActionFileDelete, "apply_patch", ops.ResultSuccess},
		{"PostToolUse apply_patch error", "PostToolUse", `{"session_id":"s1","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Add File: x.go\n+a\n*** End Patch"},"tool_response":{"error":"conflict"}}`, ops.ActionFileWrite, "apply_patch", ops.ResultError},
		{"PreToolUse MCP", "PreToolUse", `{"session_id":"s1","tool_name":"mcp__fs__read_file","tool_input":{"path":"/x"}}`, ops.ActionToolUse, "mcp__fs__read_file", ops.ResultSuccess},
		{"PostToolUse error", "PostToolUse", `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"false"},"tool_response":{"error":"boom"}}`, ops.ActionCommandExec, "Bash", ops.ResultError},
		{"UserPromptSubmit", "UserPromptSubmit", `{"session_id":"s1","prompt":"fix bug"}`, ops.ActionToolUse, "UserPromptSubmit", ops.ResultSuccess},
		{"Stop", "Stop", `{"session_id":"s1","last_assistant_message":"done"}`, ops.ActionSessionEnd, "", ops.ResultSuccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := parseHookEvent(tt.hookType, []byte(tt.raw))
			if err != nil {
				t.Fatalf("parseHookEvent: %v", err)
			}
			if e.AgentName != AgentName {
				t.Errorf("AgentName=%q want %q", e.AgentName, AgentName)
			}
			if e.ActionType != tt.wantAction {
				t.Errorf("ActionType=%q want %q", e.ActionType, tt.wantAction)
			}
			if e.ToolName != tt.wantTool {
				t.Errorf("ToolName=%q want %q", e.ToolName, tt.wantTool)
			}
			if e.ResultStatus != tt.wantResult {
				t.Errorf("ResultStatus=%q want %q", e.ResultStatus, tt.wantResult)
			}
		})
	}
}

// `codex exec` fires hooks without piping a payload to stdin, so the adapter
// is handed empty rawData for a real event. It must synthesise a minimal
// event from the hook type on argv rather than failing the unmarshal.
func TestParseHookEvent_EmptyPayload(t *testing.T) {
	t.Setenv("CODEX_SESSION_ID", "")
	for _, ht := range []struct {
		hookType   string
		wantAction ops.ActionType
	}{
		{"SessionStart", ops.ActionSessionStart},
		{"PostToolUse", ops.ActionToolUse},
		{"UserPromptSubmit", ops.ActionToolUse},
		{"Stop", ops.ActionSessionEnd},
	} {
		t.Run(ht.hookType, func(t *testing.T) {
			e, err := parseHookEvent(ht.hookType, nil)
			if err != nil {
				t.Fatalf("parseHookEvent(empty): %v", err)
			}
			if e == nil {
				t.Fatal("expected a minimal event, got nil")
			}
			if e.AgentName != AgentName {
				t.Errorf("AgentName=%q want %q", e.AgentName, AgentName)
			}
			if e.ActionType != ht.wantAction {
				t.Errorf("ActionType=%q want %q", e.ActionType, ht.wantAction)
			}
		})
	}
}

func TestSessionEnvOverride(t *testing.T) {
	t.Setenv("CODEX_SESSION_ID", "env-session")
	id, agentID := resolveSession("payload-session")
	want := ops.NewSession(AgentName, "env-session", time.Time{}).ID
	if id != want {
		t.Errorf("env override ignored: got %s want %s", id, want)
	}
	if agentID != "env-session" {
		t.Errorf("agentSessionID=%q want env-session", agentID)
	}
}

func TestInstallRoundTrip(t *testing.T) {
	setupCodexHome(t)
	a := &Adapter{}
	if _, err := a.Install(ops.InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	st, _ := a.Status()
	if !st.Installed || !st.Valid {
		t.Errorf("Status=%+v want installed+valid", st)
	}
	if _, err := a.Uninstall(ops.UninstallOptions{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	st2, _ := a.Status()
	if st2.Installed {
		t.Errorf("still installed after uninstall: %+v", st2)
	}
}
