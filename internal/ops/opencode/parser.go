package opencode

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// toolEventInput is the payload the plugin sends for tool.execute.* events.
type toolEventInput struct {
	HookType  string         `json:"hook_type"`
	SessionID string         `json:"session_id"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Result    map[string]any `json:"result"`
	Cwd       string         `json:"cwd"`
}

// sessionEventInput is the payload for session.* events.
type sessionEventInput struct {
	HookType   string         `json:"hook_type"`
	Properties map[string]any `json:"properties"`
	Cwd        string         `json:"cwd"`
}

// toolActionType maps an OpenCode tool name (lowercase) to an ActionType.
func toolActionType(tool string) ops.ActionType {
	switch tool {
	case "read", "grep", "glob", "list":
		return ops.ActionFileRead
	case "write", "edit", "patch":
		return ops.ActionFileWrite
	case "bash":
		return ops.ActionCommandExec
	default:
		return ops.ActionToolUse
	}
}

func parseHookEvent(hookType string, rawData []byte) (*ops.Event, error) {
	switch hookType {
	case "tool.execute.before":
		return parseToolEvent(rawData, false)
	case "tool.execute.after":
		return parseToolEvent(rawData, true)
	case "session.created":
		return parseSessionEvent(rawData, ops.ActionSessionStart)
	case "session.idle":
		return parseSessionEvent(rawData, ops.ActionSessionEnd)
	case "session.error":
		return parseSessionEvent(rawData, ops.ActionNotification)
	default:
		id, _ := deriveSession("")
		return newEvent(id, "", ops.ActionUnknown, "", rawData), nil
	}
}

func parseToolEvent(rawData []byte, isAfter bool) (*ops.Event, error) {
	var in toolEventInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse opencode tool event: %w", err)
	}
	sessionID, agentSessionID := deriveSession(in.SessionID)
	tool := strings.ToLower(in.Tool)
	action := toolActionType(tool)

	e := newEvent(sessionID, agentSessionID, action, in.Cwd, rawData)
	e.ToolName = tool
	e.SkillRef = ops.SkillRefFromTool(tool, in.Args)

	var result map[string]any
	if isAfter {
		result = in.Result
	}
	if err := setToolPayload(e, action, in.Args, result); err != nil {
		return nil, err
	}
	return e, nil
}

func parseSessionEvent(rawData []byte, action ops.ActionType) (*ops.Event, error) {
	var in sessionEventInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse opencode session event: %w", err)
	}
	sessionIDStr, _ := in.Properties["sessionId"].(string)
	sessionID, agentSessionID := deriveSession(sessionIDStr)

	e := newEvent(sessionID, agentSessionID, action, in.Cwd, rawData)
	switch action {
	case ops.ActionSessionStart:
		if err := e.SetPayload(ops.SessionPayload{}); err != nil {
			return nil, err
		}
	case ops.ActionSessionEnd:
		if err := e.SetPayload(ops.SessionPayload{Reason: "idle"}); err != nil {
			return nil, err
		}
	case ops.ActionNotification:
		msg, _ := in.Properties["message"].(string)
		if err := e.SetPayload(ops.NotificationPayload{Title: "session.error", Message: trunc(msg, 500)}); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func setToolPayload(e *ops.Event, action ops.ActionType, args, result map[string]any) error {
	switch action {
	case ops.ActionFileRead:
		return e.SetPayload(ops.FileReadPayload{Path: firstString(args, "filePath", "path", "file_path")})
	case ops.ActionFileWrite:
		return e.SetPayload(ops.FileWritePayload{Path: firstString(args, "filePath", "path", "file_path")})
	case ops.ActionCommandExec:
		p := ops.CommandExecPayload{}
		p.Command, _ = args["command"].(string)
		if result != nil {
			if out, ok := result["output"].(string); ok {
				p.Stdout = trunc(out, 500)
			}
		}
		return e.SetPayload(p)
	default:
		p := ops.ToolUsePayload{Input: args}
		if result != nil {
			if data, err := json.Marshal(result); err == nil {
				p.Output = string(data)
			}
		}
		return e.SetPayload(p)
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
