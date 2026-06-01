package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/ops"
)

func TestParseHookEvent_NoSkillRef(t *testing.T) {
	// Copilot declares skills in the agent profile and emits no discrete
	// skill tool-call, so SkillRef is always empty (path-only attribution).
	e, err := parseHookEvent("preToolUse",
		[]byte(`{"cwd":"/repo","toolName":"bash","toolArgs":"{\"command\":\"ls\"}"}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.SkillRef != "" {
		t.Errorf("SkillRef=%q want empty (copilot is path-only)", e.SkillRef)
	}
}

func setupCopilotHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QVR_HOME", filepath.Join(home, ".quiver"))
	t.Setenv("COPILOT_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".copilot"), 0o755); err != nil {
		t.Fatalf("mkdir .copilot: %v", err)
	}
	return home
}

func TestParseHookEvent(t *testing.T) {
	tests := []struct {
		name       string
		hookType   string
		raw        string
		wantAction ops.ActionType
		wantResult ops.ResultStatus
		wantTarget string // command or path
	}{
		{
			name:       "preToolUse bash → command_exec",
			hookType:   "preToolUse",
			raw:        `{"cwd":"/repo","toolName":"bash","toolArgs":"{\"command\":\"ls -la\"}"}`,
			wantAction: ops.ActionCommandExec,
			wantResult: ops.ResultSuccess,
			wantTarget: "ls -la",
		},
		{
			name:       "postToolUse write → file_write",
			hookType:   "postToolUse",
			raw:        `{"cwd":"/repo","toolName":"write","toolArgs":"{\"path\":\"/repo/a.go\"}"}`,
			wantAction: ops.ActionFileWrite,
			wantResult: ops.ResultSuccess,
			wantTarget: "/repo/a.go",
		},
		{
			name:       "userPromptSubmitted → tool_use",
			hookType:   "userPromptSubmitted",
			raw:        `{"cwd":"/repo","prompt":"fix the bug"}`,
			wantAction: ops.ActionToolUse,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "sessionStart",
			hookType:   "sessionStart",
			raw:        `{"cwd":"/repo"}`,
			wantAction: ops.ActionSessionStart,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "errorOccurred → error",
			hookType:   "errorOccurred",
			raw:        `{"cwd":"/repo","error":"boom"}`,
			wantAction: ops.ActionToolUse,
			wantResult: ops.ResultError,
		},
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
			if e.ResultStatus != tt.wantResult {
				t.Errorf("ResultStatus=%q want %q", e.ResultStatus, tt.wantResult)
			}
			if tt.wantTarget != "" {
				if got := payloadTarget(e); got != tt.wantTarget {
					t.Errorf("target=%q want %q", got, tt.wantTarget)
				}
			}
		})
	}
}

// TestInstallProducesValidHookFile verifies the on-disk quiver.json matches
// the Copilot CLI hook schema (version + per-type command entries with both
// bash and powershell keys).
func TestInstallProducesValidHookFile(t *testing.T) {
	home := setupCopilotHome(t)
	a := &Adapter{}
	if _, err := a.Install(ops.InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	path := filepath.Join(home, ".copilot", "hooks", "quiver.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("quiver.json not written: %v", err)
	}
	var hf hooksFile
	if err := json.Unmarshal(data, &hf); err != nil {
		t.Fatalf("invalid quiver.json: %v", err)
	}
	if hf.Version != 1 {
		t.Errorf("version=%d want 1", hf.Version)
	}
	entry := hf.Hooks["preToolUse"]
	if len(entry) != 1 {
		t.Fatalf("preToolUse entries=%d want 1", len(entry))
	}
	if entry[0].Type != "command" {
		t.Errorf("type=%q want command", entry[0].Type)
	}
	if !strings.Contains(entry[0].Bash, "_hook copilot preToolUse") {
		t.Errorf("bash command wrong: %q", entry[0].Bash)
	}
	if !strings.Contains(entry[0].Powershell, "_hook copilot preToolUse") {
		t.Errorf("powershell command wrong: %q", entry[0].Powershell)
	}

	st, _ := a.Status()
	if !st.Installed || !st.Valid {
		t.Errorf("Status=%+v want installed+valid", st)
	}

	if _, err := a.Uninstall(ops.UninstallOptions{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("quiver.json survived uninstall")
	}
}

func payloadTarget(e *ops.Event) string {
	switch e.ActionType {
	case ops.ActionCommandExec:
		var p ops.CommandExecPayload
		if e.DecodePayload(&p) == nil {
			return p.Command
		}
	case ops.ActionFileWrite:
		var p ops.FileWritePayload
		if e.DecodePayload(&p) == nil {
			return p.Path
		}
	case ops.ActionFileRead:
		var p ops.FileReadPayload
		if e.DecodePayload(&p) == nil {
			return p.Path
		}
	}
	return ""
}
