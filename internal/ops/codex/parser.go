package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
}

type toolInput struct {
	hookInput
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolResponse map[string]any `json:"tool_response"`
}

type promptInput struct {
	hookInput
	Prompt string `json:"prompt"`
}

type stopInput struct {
	hookInput
	LastAssistantMessage string `json:"last_assistant_message"`
}

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

func parseHookEvent(hookType string, rawData []byte) (*ops.Event, error) {
	// Per the Codex hooks contract, every command hook receives one JSON object
	// on stdin (session_id, cwd, hook_event_name, tool_name, tool_input, ...).
	// In practice a degraded invocation — a hook run under Codex's workspace
	// sandbox, or a misconfigured wiring — can reach us with an empty body. Fall
	// back to an empty object so every json.Unmarshal below succeeds with zero
	// values and we still record a minimal event keyed off the hook type passed
	// on argv (correlated by CODEX_SESSION_ID when the env exposes it), rather
	// than failing the unmarshal and dropping the trail entirely.
	if len(rawData) == 0 {
		rawData = []byte("{}")
	}
	var base hookInput
	if err := json.Unmarshal(rawData, &base); err != nil {
		return nil, fmt.Errorf("parse codex hook input: %w", err)
	}
	eventName := hookType
	if eventName == "" {
		eventName = base.HookEventName
	}
	sessionID, agentSessionID := resolveSession(base.SessionID)

	switch eventName {
	case "SessionStart":
		e := newEvent(sessionID, agentSessionID, ops.ActionSessionStart, base.Cwd, rawData)
		if err := e.SetPayload(ops.SessionPayload{}); err != nil {
			return nil, err
		}
		return e, nil
	case "PreToolUse":
		return parseTool(sessionID, agentSessionID, rawData, false)
	case "PostToolUse":
		return parseTool(sessionID, agentSessionID, rawData, true)
	case "UserPromptSubmit":
		var in promptInput
		if err := json.Unmarshal(rawData, &in); err != nil {
			return nil, fmt.Errorf("parse UserPromptSubmit: %w", err)
		}
		e := newEvent(sessionID, agentSessionID, ops.ActionToolUse, in.Cwd, rawData)
		e.ToolName = "UserPromptSubmit"
		if err := e.SetPayload(ops.ToolUsePayload{Output: trunc(in.Prompt, 500)}); err != nil {
			return nil, err
		}
		return e, nil
	case "Stop":
		var in stopInput
		if err := json.Unmarshal(rawData, &in); err != nil {
			return nil, fmt.Errorf("parse Stop: %w", err)
		}
		e := newEvent(sessionID, agentSessionID, ops.ActionSessionEnd, in.Cwd, rawData)
		if err := e.SetPayload(ops.SessionPayload{Reason: trunc(in.LastAssistantMessage, 200)}); err != nil {
			return nil, err
		}
		return e, nil
	default:
		return newEvent(sessionID, agentSessionID, ops.ActionUnknown, base.Cwd, rawData), nil
	}
}

func parseTool(sessionID uuid.UUID, agentSessionID string, rawData []byte, isPost bool) (*ops.Event, error) {
	var in toolInput
	if err := json.Unmarshal(rawData, &in); err != nil {
		return nil, fmt.Errorf("parse codex tool hook: %w", err)
	}

	// Codex performs every file edit through a single `apply_patch` tool whose
	// `tool_input.command` carries a V4A patch body — NOT the discrete
	// file_path/content fields a Claude-Code Write/Edit would send. Per the
	// Codex hooks contract: "Bash and apply_patch use tool_input.command".
	// Parse the patch header so the affected path and add/update/delete intent
	// are recorded, instead of dropping a pathless generic tool_use row.
	if strings.EqualFold(in.ToolName, "apply_patch") {
		return parseApplyPatch(sessionID, agentSessionID, rawData, &in, isPost)
	}

	action := toolActionType(in.ToolName)
	e := newEvent(sessionID, agentSessionID, action, in.Cwd, rawData)
	e.ToolName = in.ToolName
	e.SkillRef = ops.SkillRefFromTool(in.ToolName, in.ToolInput)

	var resp map[string]any
	if isPost {
		resp = in.ToolResponse
	}
	if err := setToolPayload(e, action, in.ToolInput, resp); err != nil {
		return nil, err
	}
	if isPost && resp != nil {
		if msg, ok := resp["error"].(string); ok && msg != "" {
			e.ResultStatus = ops.ResultError
			e.ErrorMessage = trunc(msg, 500)
		}
	}
	return e, nil
}

// parseApplyPatch records a Codex `apply_patch` tool call. The patch text is in
// tool_input.command; its V4A header (`*** Add/Update/Delete File: <path>`)
// names the target file and the operation. A multi-file patch is recorded under
// its first file directive.
func parseApplyPatch(sessionID uuid.UUID, agentSessionID string, rawData []byte, in *toolInput, isPost bool) (*ops.Event, error) {
	patch, _ := in.ToolInput["command"].(string)
	op, path := applyPatchTarget(patch)

	action := ops.ActionFileWrite
	if op == "delete" {
		action = ops.ActionFileDelete
	}
	e := newEvent(sessionID, agentSessionID, action, in.Cwd, rawData)
	e.ToolName = in.ToolName

	if action == ops.ActionFileDelete {
		if err := e.SetPayload(ops.FileDeletePayload{Path: path}); err != nil {
			return nil, err
		}
	} else {
		if err := e.SetPayload(ops.FileWritePayload{
			Path:           path,
			Created:        op == "add",
			ContentPreview: trunc(patch, 200),
		}); err != nil {
			return nil, err
		}
	}
	if isPost {
		if msg, ok := in.ToolResponse["error"].(string); ok && msg != "" {
			e.ResultStatus = ops.ResultError
			e.ErrorMessage = trunc(msg, 500)
		}
	}
	return e, nil
}

// applyPatchTarget scans a V4A patch body for its first file directive and
// returns the operation ("add"|"update"|"delete") and target path. It returns
// ("", "") when no directive is found (e.g. an empty payload on a degraded hook
// invocation), leaving the event recorded with action file_write and no path.
func applyPatchTarget(patch string) (op, path string) {
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			return "add", strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
		case strings.HasPrefix(line, "*** Update File:"):
			return "update", strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
		case strings.HasPrefix(line, "*** Delete File:"):
			return "delete", strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
		}
	}
	return "", ""
}

func setToolPayload(e *ops.Event, action ops.ActionType, toolIn, toolResp map[string]any) error {
	switch action {
	case ops.ActionFileRead:
		p := ops.FileReadPayload{Path: firstString(toolIn, "file_path", "path")}
		p.Pattern, _ = toolIn["pattern"].(string)
		return e.SetPayload(p)
	case ops.ActionFileWrite:
		p := ops.FileWritePayload{Path: firstString(toolIn, "file_path", "path")}
		if s, ok := toolIn["content"].(string); ok {
			p.ContentPreview = trunc(s, 200)
			p.Created = true
		}
		return e.SetPayload(p)
	case ops.ActionCommandExec:
		p := ops.CommandExecPayload{}
		p.Command, _ = toolIn["command"].(string)
		if toolResp != nil {
			if out, ok := toolResp["output"].(string); ok {
				p.Stdout = trunc(out, 500)
			}
		}
		return e.SetPayload(p)
	default:
		p := ops.ToolUsePayload{Input: toolIn}
		if toolResp != nil {
			if data, err := json.Marshal(toolResp); err == nil {
				p.Output = string(data)
			}
		}
		return e.SetPayload(p)
	}
}

// resolveSession honours the CODEX_SESSION_ID env override, then the
// payload's session_id, deriving the canonical deterministic UUID.
func resolveSession(rawSessionID string) (uuid.UUID, string) {
	if env := os.Getenv("CODEX_SESSION_ID"); env != "" {
		rawSessionID = env
	}
	if rawSessionID == "" {
		return uuid.New(), ""
	}
	if parsed, err := uuid.Parse(rawSessionID); err == nil {
		return parsed, rawSessionID
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(rawSessionID)), rawSessionID
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
