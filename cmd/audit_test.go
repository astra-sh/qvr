package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/ops"
)

// runRoot drives the root command with the given args, capturing stdout.
// The output.Printer writes to os.Stdout directly (not cmd.OutOrStdout), so
// we swap os.Stdout/os.Stderr for pipes and drain them concurrently to
// avoid blocking on a full pipe buffer.
func runRoot(t *testing.T, stdin []byte, args ...string) (string, string, error) {
	t.Helper()
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr

	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() { b, _ := io.ReadAll(rOut); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errCh <- string(b) }()

	if stdin != nil {
		rootCmd.SetIn(bytes.NewReader(stdin))
	}
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = origOut, origErr
	stdout, stderr := <-outCh, <-errCh

	t.Cleanup(func() {
		rootCmd.SetIn(os.Stdin)
		rootCmd.SetArgs(nil)
	})
	return stdout, stderr, err
}

// TestAudit_ClaudeCodeEventAttributed feeds a real Claude Code PostToolUse
// hook through the funnel and asserts the stored event is attributed to the
// installed skill, with agent_name=claude-code.
func TestAudit_ClaudeCodeEventAttributed(t *testing.T) {
	worktree, readEvents := isolatedHome(t, true)

	raw, _ := json.Marshal(map[string]any{
		"session_id": "sess-xyz",
		"cwd":        worktree,
		"tool_name":  "Write",
		"tool_input": map[string]any{
			"file_path": filepath.Join(worktree, "SKILL.md"),
			"content":   "hello world",
		},
		"tool_response": map[string]any{"success": true},
	})

	stdout, stderr, err := runHookCmd(t, raw, "claude-code", "PostToolUse")
	if err != nil {
		t.Fatalf("hook: err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}

	events := readEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	e := events[0]
	if e.AgentName != "claude-code" {
		t.Errorf("AgentName=%q want claude-code", e.AgentName)
	}
	if e.SkillName != "foo" {
		t.Errorf("SkillName=%q want foo", e.SkillName)
	}
	if e.ActionType != ops.ActionFileWrite {
		t.Errorf("ActionType=%q want file_write", e.ActionType)
	}
	if e.ToolName != "Write" {
		t.Errorf("ToolName=%q want Write", e.ToolName)
	}
}

// TestAudit_LogsCommand feeds an event then queries it back through the
// `qvr audit logs` command in JSON mode.
func TestAudit_LogsCommand(t *testing.T) {
	worktree, _ := isolatedHome(t, true)

	raw, _ := json.Marshal(map[string]any{
		"session_id": "sess-logs",
		"cwd":        worktree,
		"tool_name":  "Read",
		"tool_input": map[string]any{"file_path": filepath.Join(worktree, "SKILL.md")},
	})
	if _, _, err := runHookCmd(t, raw, "claude-code", "PostToolUse"); err != nil {
		t.Fatalf("hook: %v", err)
	}

	stdout, stderr, err := runRoot(t, nil, "audit", "logs", "--output", "json")
	if err != nil {
		t.Fatalf("audit logs: err=%v stderr=%q", err, stderr)
	}
	var events []*ops.Event
	if err := json.Unmarshal([]byte(stdout), &events); err != nil {
		t.Fatalf("parse logs json: %v\n%s", err, stdout)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event from logs; got %d", len(events))
	}
	if events[0].SkillName != "foo" || events[0].AgentName != "claude-code" {
		t.Errorf("unexpected event: skill=%q agent=%q", events[0].SkillName, events[0].AgentName)
	}

	// Filtering by a different skill yields nothing.
	stdout2, _, err := runRoot(t, nil, "audit", "logs", "--skill", "nonexistent", "--output", "json")
	if err != nil {
		t.Fatalf("audit logs filtered: %v", err)
	}
	if strings.TrimSpace(stdout2) != "[]" && strings.TrimSpace(stdout2) != "null" {
		t.Errorf("expected empty result for unknown skill; got %s", stdout2)
	}
}

// TestAudit_SessionsAndExport feeds an event, then exercises the sessions
// and export read commands.
func TestAudit_SessionsAndExport(t *testing.T) {
	worktree, _ := isolatedHome(t, true)

	raw, _ := json.Marshal(map[string]any{
		"session_id": "sess-se",
		"cwd":        worktree,
		"tool_name":  "Write",
		"tool_input": map[string]any{"file_path": filepath.Join(worktree, "SKILL.md"), "content": "x"},
	})
	if _, _, err := runHookCmd(t, raw, "claude-code", "PostToolUse"); err != nil {
		t.Fatalf("hook: %v", err)
	}

	// sessions (json)
	stdout, _, err := runRoot(t, nil, "audit", "sessions", "--output", "json")
	if err != nil {
		t.Fatalf("audit sessions: %v", err)
	}
	if !strings.Contains(stdout, "claude-code") {
		t.Errorf("sessions output missing agent: %s", stdout)
	}

	// export to a file
	out := filepath.Join(t.TempDir(), "trail.jsonl")
	if _, _, err := runRoot(t, nil, "audit", "export", "-o", out); err != nil {
		t.Fatalf("audit export: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 exported line; got %d", len(lines))
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("export line not JSON: %v", err)
	}
	if ev["$schema"] == nil {
		t.Error("exported event missing $schema")
	}
	if ev["skill_name"] != "foo" {
		t.Errorf("exported skill_name=%v want foo", ev["skill_name"])
	}
}

// TestAudit_StatusRuns verifies the status command runs and lists agents.
func TestAudit_StatusRuns(t *testing.T) {
	_, _ = isolatedHome(t, true)
	stdout, _, err := runRoot(t, nil, "audit", "status", "--output", "json")
	if err != nil {
		t.Fatalf("audit status: %v", err)
	}
	// All five adapters should appear.
	for _, agent := range []string{"claude-code", "cursor", "codex", "opencode", "copilot"} {
		if !strings.Contains(stdout, agent) {
			t.Errorf("status missing agent %q in: %s", agent, stdout)
		}
	}
}

// TestAudit_EnableDisable toggles the config flag through the commands.
func TestAudit_EnableDisable(t *testing.T) {
	_, _ = isolatedHome(t, false)

	if _, _, err := runRoot(t, nil, "audit", "enable"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	stdout, _, err := runRoot(t, nil, "audit", "logs", "--output", "json")
	if err != nil {
		t.Fatalf("logs after enable: %v", err)
	}
	// Empty DB → empty/null result, but the command must succeed.
	_ = stdout

	if _, _, err := runRoot(t, nil, "audit", "disable"); err != nil {
		t.Fatalf("disable: %v", err)
	}
}
