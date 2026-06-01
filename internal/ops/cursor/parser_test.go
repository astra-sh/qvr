package cursor

import (
	"testing"

	"github.com/raks097/quiver/internal/ops"
)

func TestParseHookEvent_NoSkillRef(t *testing.T) {
	// Cursor surfaces no discrete skill tool-call, so SkillRef is always
	// empty — attribution relies on the path signal instead.
	e, err := parseHookEvent("preToolUse",
		[]byte(`{"conversation_id":"c1","cwd":"/repo","tool_name":"read_file","tool_input":{"path":"/repo/x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.SkillRef != "" {
		t.Errorf("SkillRef=%q want empty (cursor is path-only)", e.SkillRef)
	}
}

func TestParseHookEvent(t *testing.T) {
	tests := []struct {
		name       string
		hookType   string
		raw        string
		wantAction ops.ActionType
		wantResult ops.ResultStatus
	}{
		{
			name:       "beforeReadFile → file_read",
			hookType:   "beforeReadFile",
			raw:        `{"conversation_id":"c1","file_path":"/repo/a.go"}`,
			wantAction: ops.ActionFileRead,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "afterFileEdit → file_write",
			hookType:   "afterFileEdit",
			raw:        `{"conversation_id":"c1","file_path":"/repo/a.go","edits":[{"old_string":"x","new_string":"y"}]}`,
			wantAction: ops.ActionFileWrite,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "beforeShellExecution → command_exec",
			hookType:   "beforeShellExecution",
			raw:        `{"conversation_id":"c1","command":"go test ./..."}`,
			wantAction: ops.ActionCommandExec,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "postToolUseFailure → error",
			hookType:   "postToolUseFailure",
			raw:        `{"conversation_id":"c1","tool_name":"run_terminal_cmd","tool_input":{"command":"false"},"error_message":"boom"}`,
			wantAction: ops.ActionCommandExec,
			wantResult: ops.ResultError,
		},
		{
			name:       "sessionStart",
			hookType:   "sessionStart",
			raw:        `{"conversation_id":"c1","session_id":"s1"}`,
			wantAction: ops.ActionSessionStart,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "stop → session_end",
			hookType:   "stop",
			raw:        `{"conversation_id":"c1","status":"completed"}`,
			wantAction: ops.ActionSessionEnd,
			wantResult: ops.ResultSuccess,
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
			if len(e.RawEvent) == 0 {
				t.Error("RawEvent not preserved")
			}
		})
	}
}

func TestInstallRoundTrip(t *testing.T) {
	home := setupCursorHome(t)
	a := &Adapter{}

	res, err := a.Install(ops.InstallOptions{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.HooksAdded) != len(hookTypes) {
		t.Errorf("HooksAdded=%d want %d", len(res.HooksAdded), len(hookTypes))
	}

	st, err := a.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
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
	_ = home
}
