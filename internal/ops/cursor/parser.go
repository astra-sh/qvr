package cursor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// hookInput is a superset of every Cursor hook payload. Cursor sends a
// distinct shape per hook type; decoding the union once and dispatching on
// hook type keeps the parser flat. Absent fields stay zero-valued.
type hookInput struct {
	ConversationID string   `json:"conversation_id"`
	GenerationID   string   `json:"generation_id"`
	HookEventName  string   `json:"hook_event_name"`
	WorkspaceRoots []string `json:"workspace_roots"`
	Cwd            string   `json:"cwd"`

	ToolName   string         `json:"tool_name"`
	ToolInput  map[string]any `json:"tool_input"`
	ToolOutput string         `json:"tool_output"`

	FilePath string `json:"file_path"`
	Edits    []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`

	Command string `json:"command"`
	Prompt  string `json:"prompt"`

	SessionID    string `json:"session_id"`
	Reason       string `json:"reason"`
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
}

// toolActionType maps a Cursor tool name to a canonical ActionType.
func toolActionType(toolName string) ops.ActionType {
	switch toolName {
	case "read_file", "Read", "list_dir", "grep", "codebase_search":
		return ops.ActionFileRead
	case "edit_file", "write", "Write", "search_replace":
		return ops.ActionFileWrite
	case "run_terminal_cmd", "Bash":
		return ops.ActionCommandExec
	default:
		return ops.ActionToolUse
	}
}

func parseHookEvent(hookType string, rawData []byte) (*ops.Event, error) {
	var in hookInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse cursor hook input: %w", err)
	}

	eventName := hookType
	if eventName == "" {
		eventName = in.HookEventName
	}

	// Cursor correlates a session via conversation_id; fall back to the
	// explicit session_id (session hooks) then generation_id.
	correlation := firstNonEmpty(in.ConversationID, in.SessionID, in.GenerationID)
	sessionID, agentSessionID := deriveSession(correlation)
	cwd := in.Cwd
	if cwd == "" && len(in.WorkspaceRoots) > 0 {
		cwd = in.WorkspaceRoots[0]
	}

	e := newEvent(sessionID, agentSessionID, ops.ActionUnknown, cwd, rawData)

	// No SkillRef: Cursor auto-injects rules/skills into context with no
	// discrete tool-call event that names them, so there is nothing to
	// extract here. Attribution falls to the universal path signal — a read
	// of an installed skill's file matches the resolver. See ops.SkillRefFromTool.

	switch eventName {
	case "beforeReadFile", "beforeTabFileRead":
		e.ActionType = ops.ActionFileRead
		if err := e.SetPayload(ops.FileReadPayload{Path: in.FilePath}); err != nil {
			return nil, err
		}
	case "afterFileEdit", "afterTabFileEdit":
		e.ActionType = ops.ActionFileWrite
		p := ops.FileWritePayload{Path: in.FilePath}
		if len(in.Edits) > 0 {
			p.OldString = trunc(in.Edits[0].OldString, 200)
			p.NewString = trunc(in.Edits[0].NewString, 200)
		}
		if err := e.SetPayload(p); err != nil {
			return nil, err
		}
	case "beforeShellExecution", "afterShellExecution":
		e.ActionType = ops.ActionCommandExec
		if err := e.SetPayload(ops.CommandExecPayload{Command: in.Command, Stdout: trunc(in.ToolOutput, 500)}); err != nil {
			return nil, err
		}
	case "preToolUse", "postToolUse":
		e.ActionType = toolActionType(in.ToolName)
		e.ToolName = in.ToolName
		if err := setToolPayload(e, e.ActionType, in.ToolInput, in.ToolOutput); err != nil {
			return nil, err
		}
	case "postToolUseFailure":
		e.ActionType = toolActionType(in.ToolName)
		e.ToolName = in.ToolName
		e.ResultStatus = ops.ResultError
		e.ErrorMessage = trunc(in.ErrorMessage, 500)
		if err := setToolPayload(e, e.ActionType, in.ToolInput, ""); err != nil {
			return nil, err
		}
	case "beforeMCPExecution", "afterMCPExecution", "beforeSubmitPrompt", "afterAgentThought":
		e.ActionType = ops.ActionToolUse
		e.ToolName = in.ToolName
		if err := e.SetPayload(ops.ToolUsePayload{Input: in.ToolInput, Output: trunc(in.Prompt, 500)}); err != nil {
			return nil, err
		}
	case "sessionStart":
		e.ActionType = ops.ActionSessionStart
		if err := e.SetPayload(ops.SessionPayload{}); err != nil {
			return nil, err
		}
	case "sessionEnd", "stop":
		e.ActionType = ops.ActionSessionEnd
		reason := in.Reason
		if reason == "" {
			reason = in.Status
		}
		if err := e.SetPayload(ops.SessionPayload{Reason: reason}); err != nil {
			return nil, err
		}
	default:
		// ActionUnknown event already constructed.
	}
	return e, nil
}

func setToolPayload(e *ops.Event, action ops.ActionType, toolInput map[string]any, output string) error {
	switch action {
	case ops.ActionFileRead:
		return e.SetPayload(ops.FileReadPayload{Path: firstString(toolInput, "file_path", "path", "target_file")})
	case ops.ActionFileWrite:
		return e.SetPayload(ops.FileWritePayload{Path: firstString(toolInput, "file_path", "path", "target_file")})
	case ops.ActionCommandExec:
		p := ops.CommandExecPayload{}
		p.Command, _ = toolInput["command"].(string)
		p.Stdout = trunc(output, 500)
		return e.SetPayload(p)
	default:
		return e.SetPayload(ops.ToolUsePayload{Input: toolInput, Output: trunc(output, 500)})
	}
}

func newEvent(sessionID uuid.UUID, agentSessionID string, action ops.ActionType, cwd string, rawData []byte) *ops.Event {
	return &ops.Event{
		ID:               uuid.New(),
		SessionID:        sessionID,
		AgentSessionID:   agentSessionID,
		Timestamp:        time.Now().UTC(),
		AgentName:        AgentName,
		WorkingDirectory: cwd,
		ActionType:       action,
		ResultStatus:     ops.ResultSuccess,
		RawEvent:         json.RawMessage(append([]byte(nil), rawData...)),
	}
}

func deriveSession(agentSessionID string) (uuid.UUID, string) {
	if agentSessionID == "" {
		return uuid.New(), ""
	}
	if parsed, err := uuid.Parse(agentSessionID); err == nil {
		return parsed, agentSessionID
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(agentSessionID)), agentSessionID
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func trunc(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
