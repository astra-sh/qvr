package claudecode

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// hookInput is the common envelope across all Claude Code hook payloads.
type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
}

type preToolUseInput struct {
	hookInput
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	AgentID   string         `json:"agent_id,omitempty"`
	AgentType string         `json:"agent_type,omitempty"`
}

type postToolUseInput struct {
	hookInput
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolResponse map[string]any `json:"tool_response"`
	AgentID      string         `json:"agent_id,omitempty"`
	AgentType    string         `json:"agent_type,omitempty"`
}

type sessionStartInput struct {
	hookInput
	Source string `json:"source"`
	Model  string `json:"model"`
}

type sessionEndInput struct {
	hookInput
	Reason string `json:"reason"`
}

type notificationInput struct {
	hookInput
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
}

type subagentInput struct {
	hookInput
	AgentID              string `json:"agent_id"`
	AgentType            string `json:"agent_type"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// toolActionType maps a Claude Code tool name to a canonical ActionType.
// Unknown tools fall through to ActionToolUse.
func toolActionType(toolName string) ops.ActionType {
	switch toolName {
	case "Read", "View", "Grep", "Glob", "LS":
		return ops.ActionFileRead
	case "Write", "Edit", "NotebookEdit":
		return ops.ActionFileWrite
	case "Bash", "Execute":
		return ops.ActionCommandExec
	default:
		return ops.ActionToolUse
	}
}

// parseHookEvent is the entry point used by Adapter.ParseEvent. It decodes
// the common envelope, derives the canonical session UUID, then dispatches
// to a per-type builder. Unknown hook types produce an ActionUnknown event
// rather than an error, so an upstream Claude change never breaks ingest.
func parseHookEvent(hookType string, rawData []byte) (*ops.Event, error) {
	var base hookInput
	if err := json.Unmarshal(rawData, &base); err != nil {
		return nil, fmt.Errorf("parse claude-code hook input: %w", err)
	}

	eventName := hookType
	if eventName == "" {
		eventName = base.HookEventName
	}
	sessionID, agentSessionID := deriveSession(base.SessionID)

	switch eventName {
	case "PreToolUse":
		return parsePreToolUse(sessionID, agentSessionID, rawData)
	case "PostToolUse":
		return parsePostToolUse(sessionID, agentSessionID, rawData, false)
	case "PostToolUseFailure":
		return parsePostToolUse(sessionID, agentSessionID, rawData, true)
	case "SessionStart":
		return parseSessionStart(sessionID, agentSessionID, rawData)
	case "SessionEnd":
		return parseSessionEnd(sessionID, agentSessionID, rawData)
	case "Notification":
		return parseNotification(sessionID, agentSessionID, rawData)
	case "SubagentStart":
		return parseSubagent(sessionID, agentSessionID, rawData, ops.ActionSubagentStart)
	case "SubagentStop":
		return parseSubagent(sessionID, agentSessionID, rawData, ops.ActionSubagentStop)
	default:
		e := newEvent(sessionID, agentSessionID, ops.ActionUnknown, base.Cwd, rawData)
		return e, nil
	}
}

// deriveSession turns the agent's session_id string into the canonical
// deterministic UUID (matching ops.NewSession) plus the original string for
// correlation. A missing id yields a fresh random UUID.
func deriveSession(agentSessionID string) (uuid.UUID, string) {
	if agentSessionID == "" {
		return uuid.New(), ""
	}
	if parsed, err := uuid.Parse(agentSessionID); err == nil {
		return parsed, agentSessionID
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(agentSessionID)), agentSessionID
}

// newEvent constructs an ops.Event with the fields every hook shares. The
// resolver fills SkillName downstream; privacy/logging run in the funnel.
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

func parsePreToolUse(sessionID uuid.UUID, agentSessionID string, rawData []byte) (*ops.Event, error) {
	var in preToolUseInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse PreToolUse: %w", err)
	}
	action := toolActionType(in.ToolName)
	e := newEvent(sessionID, agentSessionID, action, in.Cwd, rawData)
	e.ToolName = in.ToolName
	e.SkillRef = ops.SkillRefFromTool(in.ToolName, in.ToolInput)
	e.SubagentID = in.AgentID
	e.SubagentType = in.AgentType
	if err := setToolPayload(e, action, in.ToolInput, nil); err != nil {
		return nil, err
	}
	return e, nil
}

func parsePostToolUse(sessionID uuid.UUID, agentSessionID string, rawData []byte, isFailure bool) (*ops.Event, error) {
	var in postToolUseInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse PostToolUse: %w", err)
	}
	action := toolActionType(in.ToolName)
	e := newEvent(sessionID, agentSessionID, action, in.Cwd, rawData)
	e.ToolName = in.ToolName
	e.SkillRef = ops.SkillRefFromTool(in.ToolName, in.ToolInput)
	e.SubagentID = in.AgentID
	e.SubagentType = in.AgentType
	if err := setToolPayload(e, action, in.ToolInput, in.ToolResponse); err != nil {
		return nil, err
	}
	if isFailure {
		e.ResultStatus = ops.ResultError
		if msg, ok := in.ToolResponse["error"].(string); ok {
			e.ErrorMessage = trunc(msg, 500)
		}
	} else {
		detectError(e, in.ToolResponse)
	}
	return e, nil
}

func parseSessionStart(sessionID uuid.UUID, agentSessionID string, rawData []byte) (*ops.Event, error) {
	var in sessionStartInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse SessionStart: %w", err)
	}
	e := newEvent(sessionID, agentSessionID, ops.ActionSessionStart, in.Cwd, rawData)
	if err := e.SetPayload(ops.SessionPayload{}); err != nil {
		return nil, err
	}
	return e, nil
}

func parseSessionEnd(sessionID uuid.UUID, agentSessionID string, rawData []byte) (*ops.Event, error) {
	var in sessionEndInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse SessionEnd: %w", err)
	}
	e := newEvent(sessionID, agentSessionID, ops.ActionSessionEnd, in.Cwd, rawData)
	if err := e.SetPayload(ops.SessionPayload{Reason: in.Reason}); err != nil {
		return nil, err
	}
	return e, nil
}

func parseNotification(sessionID uuid.UUID, agentSessionID string, rawData []byte) (*ops.Event, error) {
	var in notificationInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse Notification: %w", err)
	}
	e := newEvent(sessionID, agentSessionID, ops.ActionNotification, in.Cwd, rawData)
	if err := e.SetPayload(ops.NotificationPayload{Title: in.NotificationType, Message: in.Message}); err != nil {
		return nil, err
	}
	return e, nil
}

func parseSubagent(sessionID uuid.UUID, agentSessionID string, rawData []byte, action ops.ActionType) (*ops.Event, error) {
	var in subagentInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse Subagent: %w", err)
	}
	e := newEvent(sessionID, agentSessionID, action, in.Cwd, rawData)
	e.SubagentID = in.AgentID
	e.SubagentType = in.AgentType
	if err := e.SetPayload(ops.SubagentPayload{Type: in.AgentType, Prompt: trunc(in.LastAssistantMessage, 500)}); err != nil {
		return nil, err
	}
	return e, nil
}

// setToolPayload attaches the typed payload matching the action. File and
// command actions get structured metadata; everything else gets the
// generic ToolUsePayload. Paths land in the payload so the funnel's
// resolver can attribute the event to a skill.
func setToolPayload(e *ops.Event, action ops.ActionType, toolInput, toolResponse map[string]any) error {
	switch action {
	case ops.ActionFileRead:
		p := ops.FileReadPayload{}
		p.Path = firstString(toolInput, "file_path", "path")
		p.Pattern, _ = toolInput["pattern"].(string)
		return e.SetPayload(p)

	case ops.ActionFileWrite:
		p := ops.FileWritePayload{}
		p.Path = firstString(toolInput, "file_path", "path")
		if s, ok := toolInput["old_string"].(string); ok {
			p.OldString = trunc(s, 200)
		}
		if s, ok := toolInput["new_string"].(string); ok {
			p.NewString = trunc(s, 200)
		}
		if s, ok := toolInput["content"].(string); ok {
			p.ContentPreview = trunc(s, 200)
			p.Created = true
		}
		return e.SetPayload(p)

	case ops.ActionCommandExec:
		p := ops.CommandExecPayload{}
		p.Command, _ = toolInput["command"].(string)
		if toolResponse != nil {
			if out, ok := toolResponse["output"].(string); ok {
				p.Stdout = trunc(out, 500)
			}
			if code, ok := toolResponse["exitCode"].(float64); ok {
				p.ExitCode = int(code)
			}
		}
		return e.SetPayload(p)

	default:
		p := ops.ToolUsePayload{Input: toolInput}
		if toolResponse != nil {
			if data, err := json.Marshal(toolResponse); err == nil {
				p.Output = string(data)
			}
		}
		return e.SetPayload(p)
	}
}

// detectError inspects a tool_response for failure signals and flips the
// event to ResultError when found. Mirrors Claude Code's loose error shapes.
func detectError(e *ops.Event, response map[string]any) {
	if response == nil {
		return
	}
	if msg, ok := response["error"].(string); ok && msg != "" {
		e.ResultStatus = ops.ResultError
		e.ErrorMessage = trunc(msg, 500)
		return
	}
	if ok, present := response["success"].(bool); present && !ok {
		e.ResultStatus = ops.ResultError
		return
	}
	if out, ok := response["output"].(string); ok {
		lower := strings.ToLower(out)
		for _, marker := range []string{"error:", "failed:", "permission denied", "command not found", "no such file"} {
			if strings.Contains(lower, marker) {
				e.ResultStatus = ops.ResultError
				e.ErrorMessage = trunc(out, 500)
				return
			}
		}
	}
}

// firstString returns the first present string value among keys.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// trunc clips s to maxLen runes-ish (bytes are fine for ASCII tool output),
// appending an ellipsis when it cuts.
func trunc(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
