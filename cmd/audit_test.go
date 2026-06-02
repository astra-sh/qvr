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

	// #143: status must report whether each agent has a span deriver, so a
	// raw-only agent isn't presented as fully observable.
	var resp struct {
		Agents []struct {
			Agent   string `json:"agent"`
			Derives bool   `json:"derives"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode status json: %v (%s)", err, stdout)
	}
	derives := map[string]bool{}
	for _, a := range resp.Agents {
		derives[a.Agent] = a.Derives
	}
	for _, a := range []string{"claude-code", "codex"} {
		if !derives[a] {
			t.Errorf("%s should report derives=true (it has a deriver)", a)
		}
	}
	for _, a := range []string{"copilot", "opencode"} {
		if derives[a] {
			t.Errorf("%s should report derives=false (raw-only, no deriver)", a)
		}
	}
}

// TestAudit_Ingest pins #148: a transcript can be recorded as a session with no
// live hook installed — ingest stores the raw rows and derives spans, and the
// session is then queryable like any captured one. It is idempotent.
func TestAudit_Ingest(t *testing.T) {
	_, _ = isolatedHome(t, true)
	transcript, _ := writeTranscript(t, t.TempDir())

	out, stderr, err := runRoot(t, nil, "audit", "ingest", "--agent", "claude-code", transcript, "--output", "json")
	if err != nil {
		t.Fatalf("ingest: err=%v stderr=%q", err, stderr)
	}
	var resp struct {
		Ingested []struct {
			Agent     string `json:"agent"`
			SessionID string `json:"session_id"`
			Lines     int    `json:"lines"`
			Spans     int    `json:"spans"`
			Error     string `json:"error"`
		} `json:"ingested"`
		Failed int `json:"failed"`
	}
	if e := json.Unmarshal([]byte(out), &resp); e != nil {
		t.Fatalf("decode ingest json: %v\n%s", e, out)
	}
	if resp.Failed != 0 || len(resp.Ingested) != 1 {
		t.Fatalf("want 1 successful ingest, got %+v", resp)
	}
	rec := resp.Ingested[0]
	if rec.Lines != 2 {
		t.Errorf("lines = %d, want 2", rec.Lines)
	}
	if rec.Spans < 1 {
		t.Errorf("spans = %d, want >=1 (LLM turn derived)", rec.Spans)
	}

	// Queryable without any hook ever installed.
	spansOut, _, err := runRoot(t, nil, "audit", "spans", "--session", rec.SessionID, "--output", "json")
	if err != nil {
		t.Fatalf("spans after ingest: %v", err)
	}
	if !strings.Contains(spansOut, "LLM") {
		t.Errorf("ingested session should derive an LLM span: %s", spansOut)
	}

	// Idempotent + incremental: re-ingesting the unchanged file adds no rows.
	out2, _, err := runRoot(t, nil, "audit", "ingest", "--agent", "claude-code", transcript, "--output", "json")
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	var resp2 struct {
		Ingested []struct {
			Lines int `json:"lines"`
		} `json:"ingested"`
	}
	if e := json.Unmarshal([]byte(out2), &resp2); e != nil {
		t.Fatalf("decode re-ingest: %v\n%s", e, out2)
	}
	if len(resp2.Ingested) != 1 || resp2.Ingested[0].Lines != 0 {
		t.Errorf("re-ingest should add 0 lines (idempotent), got %+v", resp2.Ingested)
	}
}

// TestAudit_IngestSniffsAgent confirms the agent format is inferred from the
// transcript when --agent is omitted (a claude transcript → claude-code).
func TestAudit_IngestSniffsAgent(t *testing.T) {
	_, _ = isolatedHome(t, true)
	transcript, _ := writeTranscript(t, t.TempDir())

	out, stderr, err := runRoot(t, nil, "audit", "ingest", transcript, "--output", "json")
	if err != nil {
		t.Fatalf("ingest (sniff): err=%v stderr=%q", err, stderr)
	}
	var resp struct {
		Ingested []struct {
			Agent string `json:"agent"`
			Error string `json:"error"`
		} `json:"ingested"`
		Failed int `json:"failed"`
	}
	if e := json.Unmarshal([]byte(out), &resp); e != nil {
		t.Fatalf("decode: %v\n%s", e, out)
	}
	if resp.Failed != 0 || len(resp.Ingested) != 1 || resp.Ingested[0].Agent != "claude-code" {
		t.Errorf("expected sniffed agent claude-code, got %+v", resp)
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
