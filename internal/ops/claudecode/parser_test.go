package claudecode

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

func TestParseHookEvent_SkillRef(t *testing.T) {
	e, err := parseHookEvent("PreToolUse",
		[]byte(`{"session_id":"s1","cwd":"/repo","tool_name":"Skill","tool_input":{"skill":"code-review"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.SkillRef != "code-review" {
		t.Errorf("SkillRef=%q want code-review", e.SkillRef)
	}
	// An ordinary tool call carries no SkillRef.
	e2, err := parseHookEvent("PreToolUse",
		[]byte(`{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e2.SkillRef != "" {
		t.Errorf("SkillRef=%q want empty for Bash", e2.SkillRef)
	}
}

func TestParseHookEvent(t *testing.T) {
	tests := []struct {
		name       string
		hookType   string
		raw        string
		wantAction ops.ActionType
		wantTool   string
		wantResult ops.ResultStatus
		wantCwd    string
		wantTarget string // path/command extracted from payload, "" to skip
	}{
		{
			name:       "PreToolUse Read → file_read",
			hookType:   "PreToolUse",
			raw:        `{"session_id":"s1","cwd":"/repo","tool_name":"Read","tool_input":{"file_path":"/repo/main.go"}}`,
			wantAction: ops.ActionFileRead,
			wantTool:   "Read",
			wantResult: ops.ResultSuccess,
			wantCwd:    "/repo",
			wantTarget: "/repo/main.go",
		},
		{
			name:       "PostToolUse Write → file_write success",
			hookType:   "PostToolUse",
			raw:        `{"session_id":"s1","cwd":"/repo","tool_name":"Write","tool_input":{"file_path":"/repo/x.txt","content":"hi"},"tool_response":{"success":true}}`,
			wantAction: ops.ActionFileWrite,
			wantTool:   "Write",
			wantResult: ops.ResultSuccess,
			wantCwd:    "/repo",
			wantTarget: "/repo/x.txt",
		},
		{
			name:       "PostToolUse Bash → command_exec",
			hookType:   "PostToolUse",
			raw:        `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls -la"},"tool_response":{"exitCode":0,"output":"ok"}}`,
			wantAction: ops.ActionCommandExec,
			wantTool:   "Bash",
			wantResult: ops.ResultSuccess,
			wantTarget: "ls -la",
		},
		{
			name:       "PostToolUseFailure → error",
			hookType:   "PostToolUseFailure",
			raw:        `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"false"},"tool_response":{"error":"boom"}}`,
			wantAction: ops.ActionCommandExec,
			wantTool:   "Bash",
			wantResult: ops.ResultError,
		},
		{
			name:       "PostToolUse error detected in output",
			hookType:   "PostToolUse",
			raw:        `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"cat x"},"tool_response":{"output":"cat: x: No such file"}}`,
			wantAction: ops.ActionCommandExec,
			wantTool:   "Bash",
			wantResult: ops.ResultError,
		},
		{
			name:       "SessionStart",
			hookType:   "SessionStart",
			raw:        `{"session_id":"s1","cwd":"/repo","source":"startup","model":"opus"}`,
			wantAction: ops.ActionSessionStart,
			wantResult: ops.ResultSuccess,
			wantCwd:    "/repo",
		},
		{
			name:       "SessionEnd",
			hookType:   "SessionEnd",
			raw:        `{"session_id":"s1","reason":"user-exit"}`,
			wantAction: ops.ActionSessionEnd,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "Notification",
			hookType:   "Notification",
			raw:        `{"session_id":"s1","message":"hi","notification_type":"info"}`,
			wantAction: ops.ActionNotification,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "SubagentStart",
			hookType:   "SubagentStart",
			raw:        `{"session_id":"s1","agent_id":"a1","agent_type":"explorer"}`,
			wantAction: ops.ActionSubagentStart,
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "Unknown tool → tool_use",
			hookType:   "PostToolUse",
			raw:        `{"session_id":"s1","tool_name":"WebSearch","tool_input":{"query":"go"}}`,
			wantAction: ops.ActionToolUse,
			wantTool:   "WebSearch",
			wantResult: ops.ResultSuccess,
		},
		{
			name:       "Unknown hook type → unknown action",
			hookType:   "SomethingNew",
			raw:        `{"session_id":"s1","cwd":"/repo"}`,
			wantAction: ops.ActionUnknown,
			wantResult: ops.ResultSuccess,
			wantCwd:    "/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := parseHookEvent(tt.hookType, []byte(tt.raw))
			if err != nil {
				t.Fatalf("parseHookEvent: %v", err)
			}
			if e.AgentName != AgentName {
				t.Errorf("AgentName = %q, want %q", e.AgentName, AgentName)
			}
			if e.ActionType != tt.wantAction {
				t.Errorf("ActionType = %q, want %q", e.ActionType, tt.wantAction)
			}
			if e.ToolName != tt.wantTool {
				t.Errorf("ToolName = %q, want %q", e.ToolName, tt.wantTool)
			}
			if e.ResultStatus != tt.wantResult {
				t.Errorf("ResultStatus = %q, want %q", e.ResultStatus, tt.wantResult)
			}
			if tt.wantCwd != "" && e.WorkingDirectory != tt.wantCwd {
				t.Errorf("WorkingDirectory = %q, want %q", e.WorkingDirectory, tt.wantCwd)
			}
			if len(e.RawEvent) == 0 {
				t.Error("RawEvent not preserved")
			}
			if tt.wantTarget != "" {
				if got := eventTargetForTest(e); got != tt.wantTarget {
					t.Errorf("payload target = %q, want %q", got, tt.wantTarget)
				}
			}
		})
	}
}

func TestDeriveSessionDeterministic(t *testing.T) {
	// Non-UUID session strings hash to a stable UUID matching ops.NewSession.
	got, agentID := deriveSession("abc-123")
	want := ops.NewSession(AgentName, "abc-123", time.Time{}).ID
	if got != want {
		t.Errorf("deriveSession id = %s, want %s", got, want)
	}
	if agentID != "abc-123" {
		t.Errorf("agentSessionID = %q, want abc-123", agentID)
	}

	// A real UUID passes through unchanged.
	u := uuid.New()
	got2, _ := deriveSession(u.String())
	if got2 != u {
		t.Errorf("deriveSession(valid uuid) = %s, want %s", got2, u)
	}
}

// eventTargetForTest mirrors the cmd-layer target extraction without
// importing the cmd package.
func eventTargetForTest(e *ops.Event) string {
	switch e.ActionType {
	case ops.ActionFileRead:
		var p ops.FileReadPayload
		if e.DecodePayload(&p) == nil {
			if p.Path != "" {
				return p.Path
			}
			return p.Pattern
		}
	case ops.ActionFileWrite:
		var p ops.FileWritePayload
		if e.DecodePayload(&p) == nil {
			return p.Path
		}
	case ops.ActionCommandExec:
		var p ops.CommandExecPayload
		if e.DecodePayload(&p) == nil {
			return p.Command
		}
	}
	return ""
}
