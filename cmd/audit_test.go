package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runRoot drives the root command, capturing stdout/stderr. The output.Printer
// writes to os.Stdout directly, so we swap it for a pipe and drain it.
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

// captureSession runs a hook against a fresh transcript and returns the
// canonical session id.
func captureSession(t *testing.T) string {
	t.Helper()
	transcript, sid := writeTranscript(t, t.TempDir())
	if _, _, err := runHookCmd(t, payloadFor(sid, transcript), "claude-code", "Stop"); err != nil {
		t.Fatalf("hook: %v", err)
	}
	return sid
}

// TestAudit_RawCommand asserts `qvr audit raw` returns the verbatim native
// lines that were captured.
func TestAudit_RawCommand(t *testing.T) {
	_, _ = isolatedHome(t, true)
	captureSession(t)

	stdout, stderr, err := runRoot(t, nil, "audit", "raw", "--source", "transcript", "--output", "json")
	if err != nil {
		t.Fatalf("audit raw: err=%v stderr=%q", err, stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("parse raw json: %v\n%s", err, stdout)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 transcript rows; got %d", len(rows))
	}
	if rows[0]["agent_name"] != "claude-code" {
		t.Errorf("agent_name=%v want claude-code", rows[0]["agent_name"])
	}
	// The stored raw is emitted inline as native JSON (a "type" field present).
	if raw, ok := rows[0]["raw"].(map[string]any); !ok || raw["type"] != "user" {
		t.Errorf("raw line not verbatim native JSON: %v", rows[0]["raw"])
	}
}

// TestAudit_SpansCommand derives spans for the captured session.
func TestAudit_SpansCommand(t *testing.T) {
	_, _ = isolatedHome(t, true)
	sid := captureSession(t)

	stdout, stderr, err := runRoot(t, nil, "audit", "spans", "--session", sid, "--output", "json")
	if err != nil {
		t.Fatalf("audit spans: err=%v stderr=%q", err, stderr)
	}
	// The captured fixture has an assistant turn, so an LLM span must be
	// derived: require BOTH the Kind field and the LLM kind value, not either.
	if !strings.Contains(stdout, `"Kind"`) {
		t.Errorf("spans output missing Kind field: %s", stdout)
	}
	if !strings.Contains(stdout, "LLM") {
		t.Errorf("spans output missing derived LLM span: %s", stdout)
	}
}

// TestAudit_SessionsAndExport exercises the sessions list and raw export.
func TestAudit_SessionsAndExport(t *testing.T) {
	_, _ = isolatedHome(t, true)
	captureSession(t)

	stdout, _, err := runRoot(t, nil, "audit", "sessions", "--output", "json")
	if err != nil {
		t.Fatalf("audit sessions: %v", err)
	}
	if !strings.Contains(stdout, "claude-code") {
		t.Errorf("sessions output missing agent: %s", stdout)
	}

	out := filepath.Join(t.TempDir(), "trail.jsonl")
	if _, _, err := runRoot(t, nil, "audit", "export", "--source", "transcript", "-o", out); err != nil {
		t.Fatalf("audit export: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 exported transcript lines; got %d", len(lines))
	}
	// Each exported line is the verbatim native JSON.
	var ln map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ln); err != nil {
		t.Fatalf("export line not JSON: %v", err)
	}
	if ln["type"] == nil {
		t.Error("exported line missing native 'type' field")
	}
}

// TestAudit_LogsCommand queries the derived span feed.
func TestAudit_LogsCommand(t *testing.T) {
	_, _ = isolatedHome(t, true)
	captureSession(t)

	stdout, stderr, err := runRoot(t, nil, "audit", "logs", "--output", "json")
	if err != nil {
		t.Fatalf("audit logs: err=%v stderr=%q", err, stderr)
	}
	if !strings.Contains(stdout, "claude-code") {
		t.Errorf("logs missing agent: %s", stdout)
	}
}

// TestAudit_StatusRuns verifies the status command lists every adapter.
func TestAudit_StatusRuns(t *testing.T) {
	_, _ = isolatedHome(t, true)
	stdout, _, err := runRoot(t, nil, "audit", "status", "--output", "json")
	if err != nil {
		t.Fatalf("audit status: %v", err)
	}
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
	if _, _, err := runRoot(t, nil, "audit", "logs", "--output", "json"); err != nil {
		t.Fatalf("logs after enable: %v", err)
	}
	if _, _, err := runRoot(t, nil, "audit", "disable"); err != nil {
		t.Fatalf("disable: %v", err)
	}
}
