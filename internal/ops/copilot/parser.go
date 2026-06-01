package copilot

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// hookInput is the Copilot CLI hook payload (stdin). toolArgs is a JSON
// *string* the agent passes for tool hooks — it must be unmarshalled again
// to read .command / file paths.
type hookInput struct {
	Timestamp int64  `json:"timestamp"` // ms epoch
	Cwd       string `json:"cwd"`
	Prompt    string `json:"prompt"`
	ToolName  string `json:"toolName"`
	ToolArgs  string `json:"toolArgs"`
	Error     string `json:"error"`
}

// toolActionType maps a Copilot tool name to a canonical ActionType.
func toolActionType(tool string) ops.ActionType {
	switch strings.ToLower(tool) {
	case "bash", "shell", "run":
		return ops.ActionCommandExec
	case "view", "read", "read_file":
		return ops.ActionFileRead
	case "str_replace_editor", "edit", "write", "create", "str_replace":
		return ops.ActionFileWrite
	default:
		return ops.ActionToolUse
	}
}

func parseHookEvent(hookType string, rawData []byte) (*ops.Event, error) {
	var in hookInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse copilot hook input: %w", err)
	}

	// Copilot hooks carry no session id. Correlate on cwd so activity in
	// one working directory groups into a single session — coarse but the
	// best signal available. Per-event attribution still works via the
	// path extracted from toolArgs.
	sessionID, agentSessionID := deriveSession(in.Cwd)
	ts := time.Now().UTC()
	if in.Timestamp > 0 {
		ts = time.UnixMilli(in.Timestamp).UTC()
	}

	switch hookType {
	case "preToolUse", "postToolUse":
		return parseTool(sessionID, agentSessionID, in, ts, rawData), nil
	case "userPromptSubmitted":
		e := newEvent(sessionID, agentSessionID, ops.ActionToolUse, in.Cwd, ts, rawData)
		e.ToolName = "userPromptSubmitted"
		if err := e.SetPayload(ops.ToolUsePayload{Output: trunc(in.Prompt, 500)}); err != nil {
			return nil, err
		}
		return e, nil
	case "sessionStart":
		e := newEvent(sessionID, agentSessionID, ops.ActionSessionStart, in.Cwd, ts, rawData)
		if err := e.SetPayload(ops.SessionPayload{}); err != nil {
			return nil, err
		}
		return e, nil
	case "sessionEnd", "agentStop":
		e := newEvent(sessionID, agentSessionID, ops.ActionSessionEnd, in.Cwd, ts, rawData)
		if err := e.SetPayload(ops.SessionPayload{Reason: hookType}); err != nil {
			return nil, err
		}
		return e, nil
	case "errorOccurred":
		e := newEvent(sessionID, agentSessionID, ops.ActionToolUse, in.Cwd, ts, rawData)
		e.ResultStatus = ops.ResultError
		e.ErrorMessage = trunc(in.Error, 500)
		e.ToolName = in.ToolName
		return e, nil
	default:
		return newEvent(sessionID, agentSessionID, ops.ActionUnknown, in.Cwd, ts, rawData), nil
	}
}

func parseTool(sessionID uuid.UUID, agentSessionID string, in hookInput, ts time.Time, rawData []byte) *ops.Event {
	// No SkillRef: Copilot declares skills in the agent profile and emits no
	// discrete skill tool-call, so there is nothing to extract. Attribution
	// falls to the universal path signal (a read of an installed skill's
	// file matches the resolver). See ops.SkillRefFromTool.
	action := toolActionType(in.ToolName)
	e := newEvent(sessionID, agentSessionID, action, in.Cwd, ts, rawData)
	e.ToolName = in.ToolName

	args := decodeToolArgs(in.ToolArgs)
	switch action {
	case ops.ActionCommandExec:
		p := ops.CommandExecPayload{}
		p.Command, _ = args["command"].(string)
		_ = e.SetPayload(p)
	case ops.ActionFileRead:
		_ = e.SetPayload(ops.FileReadPayload{Path: firstString(args, "path", "file_path", "filePath")})
	case ops.ActionFileWrite:
		_ = e.SetPayload(ops.FileWritePayload{Path: firstString(args, "path", "file_path", "filePath")})
	default:
		_ = e.SetPayload(ops.ToolUsePayload{Input: args})
	}
	return e
}

// decodeToolArgs parses the toolArgs JSON string. Returns an empty map on
// any error (Copilot may send a non-object or omit it).
func decodeToolArgs(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}

func newEvent(sessionID uuid.UUID, agentSessionID string, action ops.ActionType, cwd string, ts time.Time, rawData []byte) *ops.Event {
	return &ops.Event{
		ID:               uuid.New(),
		SessionID:        sessionID,
		AgentSessionID:   agentSessionID,
		Timestamp:        ts,
		AgentName:        AgentName,
		WorkingDirectory: cwd,
		ActionType:       action,
		ResultStatus:     ops.ResultSuccess,
		RawEvent:         json.RawMessage(append([]byte(nil), rawData...)),
	}
}

func deriveSession(key string) (uuid.UUID, string) {
	if key == "" {
		return uuid.New(), ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)), key
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
