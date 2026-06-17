package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAudit_AnnotateRoundTrip drives the full CLI: discover a seeded session,
// record a human verdict on it, then read it back via `audit annotations`.
func TestAudit_AnnotateRoundTrip(t *testing.T) {
	home, _ := isolatedHome(t, true)
	t.Setenv("HOME", home)
	seedClaudeStore(t, home)

	if _, stderr, err := runRoot(t, nil, "audit", "discover", "--agent", "claude", "--output", "json"); err != nil {
		t.Fatalf("discover: err=%v stderr=%q", err, stderr)
	}

	// Grab the discovered session id.
	sessionsOut, _, err := runRoot(t, nil, "audit", "sessions", "--output", "json")
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	var sessions []struct {
		SessionID string `json:"session_id"`
	}
	if e := json.Unmarshal([]byte(sessionsOut), &sessions); e != nil || len(sessions) == 0 {
		t.Fatalf("decode sessions: %v\n%s", e, sessionsOut)
	}
	sid := sessions[0].SessionID

	// Record a per-skill verdict.
	out, stderr, err := runRoot(t, nil, "audit", "annotate", sid,
		"--skill", "code-review", "--outcome", "bad", "--note", "missed the bug", "--output", "json")
	if err != nil {
		t.Fatalf("annotate: err=%v stderr=%q", err, stderr)
	}
	var wrote struct {
		Outcome string `json:"outcome"`
		Skill   string `json:"skill"`
		Author  string `json:"author"`
	}
	if e := json.Unmarshal([]byte(out), &wrote); e != nil {
		t.Fatalf("decode annotate json: %v\n%s", e, out)
	}
	if wrote.Outcome != "bad" || wrote.Skill != "code-review" || wrote.Author == "" {
		t.Fatalf("annotate echo = %+v, want outcome=bad skill=code-review author!=\"\"", wrote)
	}

	// Read it back.
	listOut, _, err := runRoot(t, nil, "audit", "annotations", "--skill", "code-review", "--output", "json")
	if err != nil {
		t.Fatalf("annotations list: %v", err)
	}
	if !strings.Contains(listOut, "missed the bug") || !strings.Contains(listOut, sid) {
		t.Errorf("annotation not listed: %s", listOut)
	}
}

// TestAudit_AnnotateRequiresOutcome pins the required --outcome flag.
func TestAudit_AnnotateRequiresOutcome(t *testing.T) {
	home, _ := isolatedHome(t, true)
	t.Setenv("HOME", home)
	if _, _, err := runRoot(t, nil, "audit", "annotate", "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Error("expected an error when --outcome is omitted")
	}
}

// TestAudit_AnnotateUnknownSession rejects a verdict on a session that was
// never captured.
func TestAudit_AnnotateUnknownSession(t *testing.T) {
	home, _ := isolatedHome(t, true)
	t.Setenv("HOME", home)
	seedClaudeStore(t, home)
	if _, _, err := runRoot(t, nil, "audit", "discover", "--agent", "claude"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	_, _, err := runRoot(t, nil, "audit", "annotate",
		"11111111-1111-1111-1111-111111111111", "--outcome", "bad")
	if err == nil {
		t.Error("expected an error annotating an unknown session")
	}
}
