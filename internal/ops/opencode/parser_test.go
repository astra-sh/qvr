package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/ops"
)

func TestParseHookEvent_SkillRef(t *testing.T) {
	e, err := parseHookEvent("tool.execute.before",
		[]byte(`{"hook_type":"tool.execute.before","session_id":"s1","tool":"skill","args":{"skill":"verify"},"cwd":"/repo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.SkillRef != "verify" {
		t.Errorf("SkillRef=%q want verify", e.SkillRef)
	}
	e2, err := parseHookEvent("tool.execute.before",
		[]byte(`{"hook_type":"tool.execute.before","session_id":"s1","tool":"bash","args":{"command":"ls"},"cwd":"/repo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if e2.SkillRef != "" {
		t.Errorf("SkillRef=%q want empty for bash", e2.SkillRef)
	}
}

func setupOpencodeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QVR_HOME", filepath.Join(home, ".quiver"))
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o755); err != nil {
		t.Fatalf("mkdir opencode: %v", err)
	}
	return home
}

func TestParseHookEvent(t *testing.T) {
	tests := []struct {
		name       string
		hookType   string
		raw        string
		wantAction ops.ActionType
		wantTool   string
	}{
		{"read → file_read", "tool.execute.before", `{"session_id":"s1","tool":"read","args":{"filePath":"/repo/a.go"},"cwd":"/repo"}`, ops.ActionFileRead, "read"},
		{"write → file_write", "tool.execute.after", `{"session_id":"s1","tool":"write","args":{"filePath":"/repo/a.go"},"result":{"output":"ok"},"cwd":"/repo"}`, ops.ActionFileWrite, "write"},
		{"bash → command_exec", "tool.execute.before", `{"session_id":"s1","tool":"bash","args":{"command":"ls"},"cwd":"/repo"}`, ops.ActionCommandExec, "bash"},
		{"session.created", "session.created", `{"properties":{"sessionId":"s1"},"cwd":"/repo"}`, ops.ActionSessionStart, ""},
		{"session.idle", "session.idle", `{"properties":{"sessionId":"s1"},"cwd":"/repo"}`, ops.ActionSessionEnd, ""},
		{"session.error", "session.error", `{"properties":{"sessionId":"s1","message":"boom"},"cwd":"/repo"}`, ops.ActionNotification, ""},
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
		})
	}
}

func TestInstallRoundTrip(t *testing.T) {
	home := setupOpencodeHome(t)
	a := &Adapter{}

	res, err := a.Install(ops.InstallOptions{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.HooksAdded) == 0 {
		t.Error("no hooks added")
	}
	pluginFile := filepath.Join(home, ".config", "opencode", "plugins", "quiver.js")
	data, err := os.ReadFile(pluginFile)
	if err != nil {
		t.Fatalf("plugin not written: %v", err)
	}
	if strings.Contains(string(data), commandMarker) {
		t.Error("command placeholder not replaced")
	}
	if !strings.Contains(string(data), "_hook") {
		t.Error("plugin missing _hook bridge")
	}

	st, _ := a.Status()
	if !st.Installed || !st.Valid {
		t.Errorf("Status=%+v want installed+valid", st)
	}

	if _, err := a.Uninstall(ops.UninstallOptions{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(pluginFile); !os.IsNotExist(err) {
		t.Error("plugin file survived uninstall")
	}
}
