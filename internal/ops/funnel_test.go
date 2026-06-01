package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/privacy"
)

// memStore is an in-memory SessionStore for funnel tests. It tracks
// every call so tests can assert the exact sequence + content.
type memStore struct {
	mu          sync.Mutex
	events      []*Event
	sessions    map[uuid.UUID]*Session
	selfAudits  []*SelfAuditEntry
	failSave    error
	failSession error
	failAudit   error
}

func newMemStore() *memStore {
	return &memStore{sessions: map[uuid.UUID]*Session{}}
}

func (m *memStore) SaveEvent(_ context.Context, e *Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSave != nil {
		return m.failSave
	}
	// Clone via JSON so tests can safely inspect the recorded event
	// even if the caller subsequently mutates it.
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var cp Event
	if err := json.Unmarshal(raw, &cp); err != nil {
		return err
	}
	m.events = append(m.events, &cp)
	return nil
}

func (m *memStore) GetSession(_ context.Context, id uuid.UUID) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSession != nil {
		return nil, m.failSession
	}
	return m.sessions[id], nil
}

func (m *memStore) UpsertSession(_ context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSession != nil {
		return m.failSession
	}
	raw, _ := json.Marshal(s)
	var cp Session
	_ = json.Unmarshal(raw, &cp)
	m.sessions[s.ID] = &cp
	return nil
}

func (m *memStore) AppendSelfAudit(_ context.Context, entry *SelfAuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAudit != nil {
		return m.failAudit
	}
	m.selfAudits = append(m.selfAudits, entry)
	return nil
}

func (m *memStore) BackfillSkill(_ context.Context, sessionID uuid.UUID, skill string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, e := range m.events {
		if e.SessionID == sessionID && e.SkillName == SkillPending {
			e.SkillName = skill
			n++
		}
	}
	return n, nil
}

func (m *memStore) DeleteSession(_ context.Context, id uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.events[:0:0]
	var n int64
	for _, e := range m.events {
		if e.SessionID == id {
			n++
			continue
		}
		kept = append(kept, e)
	}
	m.events = kept
	delete(m.sessions, id)
	return n, nil
}

func (m *memStore) DeleteSkilllessSessions(_ context.Context, olderThan time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, s := range m.sessions {
		if len(s.SkillsTouched) == 0 && s.StartedAt.Before(olderThan) {
			delete(m.sessions, id)
			kept := m.events[:0:0]
			for _, e := range m.events {
				if e.SessionID != id {
					kept = append(kept, e)
				}
			}
			m.events = kept
			n++
		}
	}
	return n, nil
}

// --- Test helpers ---

// funnelHarness wires a complete funnel around a memStore and a
// lockfile with the given skill entries. Returns the funnel plus the
// memStore (for assertions) plus the map of skill name → worktree dir.
type funnelHarness struct {
	funnel *Funnel
	store  *memStore
	wts    map[string]string
	cfg    *config.Config
}

func newFunnelHarness(t *testing.T, cfg *config.Config, entries ...*model.LockEntry) *funnelHarness {
	t.Helper()
	fx := newFixture(t, entries...)
	res, err := NewResolver(fx.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	checker, err := privacy.Default(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mem := newMemStore()
	ad, err := GetAdapter("test-generic")
	if err != nil {
		ad = &testGeneric{}
		Register(ad)
		t.Cleanup(func() { Unregister("test-generic") })
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.Ops.Enabled = true
	ApplyDefaults(cfg)
	fn, err := NewFunnel(FunnelDeps{
		Config:   cfg,
		Adapter:  ad,
		Resolver: res,
		Privacy:  checker,
		Store:    mem,
		Clock:    func() time.Time { return time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &funnelHarness{funnel: fn, store: mem, wts: fx.worktrees, cfg: cfg}
}

// testGeneric is a minimal pass-through adapter for funnel tests — it
// accepts canonical JSON as-is and fills in the same defaults as the
// real generic adapter, without the init() global-registry side-effect.
type testGeneric struct{}

func (testGeneric) Name() string { return "test-generic" }
func (testGeneric) ParseEvent(_ context.Context, hookType string, rawData []byte) (*Event, error) {
	if len(rawData) == 0 {
		return nil, errors.New("empty")
	}
	var e Event
	if err := json.Unmarshal(rawData, &e); err != nil {
		return nil, err
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.SessionID == uuid.Nil {
		if e.AgentSessionID == "" {
			return nil, errors.New("missing session ids")
		}
		e.SessionID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(e.AgentSessionID))
	}
	if e.ActionType == "" {
		e.ActionType = ActionUnknown
	}
	if e.ResultStatus == "" {
		e.ResultStatus = ResultSuccess
	}
	e.RawEvent = json.RawMessage(append([]byte(nil), rawData...))
	return &e, nil
}

// --- Happy path ---

func TestFunnel_Happy_PersistsEventAndSession(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	path := filepath.Join(h.wts["foo"], "SKILL.md")

	raw := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "sess-abc",
		"action_type":      "file_read",
		"payload":          map[string]any{"path": path},
	})
	if err := h.funnel.Ingest(context.Background(), "PostToolUse", raw); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(h.store.events))
	}
	e := h.store.events[0]
	if e.SkillName != "foo" {
		t.Errorf("SkillName=%q want foo", e.SkillName)
	}
	if len(h.store.sessions) != 1 {
		t.Errorf("expected 1 session; got %d", len(h.store.sessions))
	}
	for _, s := range h.store.sessions {
		if s.FilesRead != 1 {
			t.Errorf("FilesRead=%d want 1", s.FilesRead)
		}
		if s.TotalActions != 1 {
			t.Errorf("TotalActions=%d want 1", s.TotalActions)
		}
	}
	if len(h.store.selfAudits) != 0 {
		t.Errorf("expected no self_audits on happy path; got %d", len(h.store.selfAudits))
	}
}

func TestFunnel_MultipleEvents_AccumulateSessionCounters(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	path := filepath.Join(h.wts["foo"], "SKILL.md")

	for i := 0; i < 3; i++ {
		raw := mustJSON(t, map[string]any{
			"agent_name":       "claude",
			"agent_session_id": "same-session",
			"action_type":      "file_read",
			"payload":          map[string]any{"path": path},
		})
		if err := h.funnel.Ingest(context.Background(), "PostToolUse", raw); err != nil {
			t.Fatal(err)
		}
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 3 {
		t.Errorf("expected 3 events; got %d", len(h.store.events))
	}
	if len(h.store.sessions) != 1 {
		t.Fatalf("expected 1 session; got %d", len(h.store.sessions))
	}
	for _, s := range h.store.sessions {
		if s.TotalActions != 3 {
			t.Errorf("TotalActions=%d want 3", s.TotalActions)
		}
		if s.FilesRead != 3 {
			t.Errorf("FilesRead=%d want 3", s.FilesRead)
		}
	}
}

// --- Disabled / per-agent disabled ---

func TestFunnel_DisabledAgent_NoPersist(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.Agents = map[string]config.OpsAgentConfig{
		"claude": {Enabled: false},
	}
	h := newFunnelHarness(t, cfg, &model.LockEntry{Name: "foo"})
	raw := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "file_read",
		"payload":          map[string]any{"path": filepath.Join(h.wts["foo"], "x")},
	})
	if err := h.funnel.Ingest(context.Background(), "x", raw); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 0 {
		t.Errorf("expected no events for disabled agent; got %d", len(h.store.events))
	}
}

// --- Parse errors ---

func TestFunnel_ParseError_RecordsSelfAudit(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	// Invalid JSON
	if err := h.funnel.Ingest(context.Background(), "x", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 0 {
		t.Errorf("expected no events; got %d", len(h.store.events))
	}
	if len(h.store.selfAudits) != 1 {
		t.Fatalf("expected 1 self_audit; got %d", len(h.store.selfAudits))
	}
	if h.store.selfAudits[0].Action != SelfAuditHookError {
		t.Errorf("expected hook_error; got %q", h.store.selfAudits[0].Action)
	}
}

// --- Unattributed event is recorded provisionally, never dropped ---

func TestFunnel_Unattributed_RecordsPending(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	// Path outside any worktree — no skill reference.
	raw := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "file_read",
		"payload":          map[string]any{"path": "/some/random/path.md"},
	})
	if err := h.funnel.Ingest(context.Background(), "x", raw); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 1 {
		t.Fatalf("expected the event to be recorded (not dropped); got %d", len(h.store.events))
	}
	if got := h.store.events[0].SkillName; got != SkillPending {
		t.Errorf("SkillName=%q want %q", got, SkillPending)
	}
	if len(h.store.selfAudits) != 0 {
		t.Errorf("expected no self_audits (no more drops); got %d", len(h.store.selfAudits))
	}
}

// --- Mid-session skill invocation back-fills the whole trace ---

func TestFunnel_SkillRef_AttributesAndBackfills(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})

	// 1) An ordinary event before any skill — recorded as pending.
	pre := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "sess-1",
		"action_type":      "command_exec",
		"payload":          map[string]any{"command": "echo before"},
	})
	if err := h.funnel.Ingest(context.Background(), "x", pre); err != nil {
		t.Fatal(err)
	}

	// 2) A skill tool-call names the active skill (SkillRef set directly).
	skill := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "sess-1",
		"action_type":      "tool_use",
		"tool_name":        "Skill",
		"skill_ref":        "foo",
	})
	if err := h.funnel.Ingest(context.Background(), "x", skill); err != nil {
		t.Fatal(err)
	}

	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 2 {
		t.Fatalf("expected 2 events; got %d", len(h.store.events))
	}
	for _, e := range h.store.events {
		if e.SkillName != "foo" {
			t.Errorf("event %s SkillName=%q want foo (whole trace attributed)", e.ActionType, e.SkillName)
		}
	}
	if len(h.store.sessions) != 1 {
		t.Fatalf("expected 1 session; got %d", len(h.store.sessions))
	}
	for _, s := range h.store.sessions {
		if len(s.SkillsTouched) != 1 || s.SkillsTouched[0] != "foo" {
			t.Errorf("SkillsTouched=%v want [foo]", s.SkillsTouched)
		}
	}
}

// --- A session can touch multiple skills ---

func TestFunnel_MultipleSkills_AllInSkillsTouched(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"}, &model.LockEntry{Name: "bar"})
	for _, name := range []string{"foo", "bar"} {
		raw := mustJSON(t, map[string]any{
			"agent_name":       "claude",
			"agent_session_id": "multi",
			"action_type":      "tool_use",
			"tool_name":        "Skill",
			"skill_ref":        name,
		})
		if err := h.funnel.Ingest(context.Background(), "x", raw); err != nil {
			t.Fatal(err)
		}
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.sessions) != 1 {
		t.Fatalf("expected 1 session; got %d", len(h.store.sessions))
	}
	for _, s := range h.store.sessions {
		if len(s.SkillsTouched) != 2 {
			t.Errorf("SkillsTouched=%v want both foo and bar", s.SkillsTouched)
		}
	}
}

// --- Skill-less session is discarded at its end only when opted in ---

func TestFunnel_SessionEnd_SkillLess_Pruned(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.PruneSkilllessSessions = true // opt in to the old noise-reduction behaviour
	h := newFunnelHarness(t, cfg, &model.LockEntry{Name: "foo"})
	// An ordinary (pending) event, then a session end with no skill.
	pre := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "ghost",
		"action_type":      "command_exec",
		"payload":          map[string]any{"command": "echo hi"},
	})
	end := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "ghost",
		"action_type":      "session_end",
	})
	if err := h.funnel.Ingest(context.Background(), "x", pre); err != nil {
		t.Fatal(err)
	}
	if err := h.funnel.Ingest(context.Background(), "SessionEnd", end); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 0 {
		t.Errorf("expected skill-less session's events to be pruned; got %d", len(h.store.events))
	}
	if len(h.store.sessions) != 0 {
		t.Errorf("expected skill-less session to be pruned; got %d", len(h.store.sessions))
	}
}

// --- Skill-less session is retained at its end by default (issue #138) ---
//
// A real `codex exec "echo hi"` fires SessionStart→…→Stop without ever
// touching an installed skill. The default must capture it, not delete the
// whole trace at Stop (which previously netted zero rows + exit 0 and was
// misread as a Codex sandbox swallowing the write).
func TestFunnel_SessionEnd_SkillLess_RetainedByDefault(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	pre := mustJSON(t, map[string]any{
		"agent_name":       "codex",
		"agent_session_id": "live",
		"action_type":      "command_exec",
		"payload":          map[string]any{"command": "echo hi"},
	})
	end := mustJSON(t, map[string]any{
		"agent_name":       "codex",
		"agent_session_id": "live",
		"action_type":      "session_end",
	})
	if err := h.funnel.Ingest(context.Background(), "PreToolUse", pre); err != nil {
		t.Fatal(err)
	}
	if err := h.funnel.Ingest(context.Background(), "Stop", end); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.sessions) != 1 {
		t.Fatalf("expected the skill-less session to be retained; got %d sessions", len(h.store.sessions))
	}
	// Both the pre-event and the terminal Stop event survive, under pending.
	if len(h.store.events) != 2 {
		t.Fatalf("expected both events retained; got %d", len(h.store.events))
	}
	for _, e := range h.store.events {
		if e.SkillName != SkillPending {
			t.Errorf("expected events under %q; got %q", SkillPending, e.SkillName)
		}
	}
	for _, s := range h.store.sessions {
		if s.EndedAt == nil {
			t.Errorf("expected EndedAt set on the retained skill-less session")
		}
	}
}

// --- Skill-bearing session survives its end ---

func TestFunnel_SessionEnd_WithSkill_Retained(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	skill := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "keep",
		"action_type":      "tool_use",
		"tool_name":        "Skill",
		"skill_ref":        "foo",
	})
	end := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "keep",
		"action_type":      "session_end",
	})
	if err := h.funnel.Ingest(context.Background(), "x", skill); err != nil {
		t.Fatal(err)
	}
	if err := h.funnel.Ingest(context.Background(), "SessionEnd", end); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.sessions) != 1 {
		t.Fatalf("expected the skill-bearing session to be retained; got %d", len(h.store.sessions))
	}
	for _, s := range h.store.sessions {
		if s.EndedAt == nil {
			t.Errorf("expected EndedAt set on retained session")
		}
	}
}

// --- Privacy: sensitive path ---

func TestFunnel_SensitivePath_StripsContent(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	envPath := filepath.Join(h.wts["foo"], ".env")
	raw := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "file_write",
		"diff_content":     "AWS_SECRET_ACCESS_KEY=realsecret",
		"payload": map[string]any{
			"path":       envPath,
			"new_string": "AWS_SECRET_ACCESS_KEY=realsecret",
		},
	})
	if err := h.funnel.Ingest(context.Background(), "PostToolUse", raw); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(h.store.events))
	}
	e := h.store.events[0]
	if !e.IsSensitive {
		t.Errorf("expected IsSensitive=true")
	}
	if e.DiffContent != "" {
		t.Errorf("expected DiffContent stripped; got %q", e.DiffContent)
	}
	if strings.Contains(string(e.Payload), "realsecret") {
		t.Errorf("secret survived in payload: %s", e.Payload)
	}
	// But the path metadata must remain.
	if !strings.Contains(string(e.Payload), filepath.Base(envPath)) {
		t.Errorf("path stripped (should have survived)")
	}

	// Session counter should reflect the sensitive action.
	for _, s := range h.store.sessions {
		if s.SensitiveActions != 1 {
			t.Errorf("SensitiveActions=%d want 1", s.SensitiveActions)
		}
	}
}

// --- Privacy: regex redaction on non-sensitive path ---

func TestFunnel_CommandWithSecret_Redacts(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	scriptPath := filepath.Join(h.wts["foo"], "deploy.sh")
	raw := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "command_exec",
		"payload": map[string]any{
			"path":    scriptPath,
			"command": "export AWS_SECRET_ACCESS_KEY=AKIAZZZZZZZZZZZZZZZZ && deploy",
		},
	})
	if err := h.funnel.Ingest(context.Background(), "x", raw); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) == 0 {
		t.Fatalf("event was not saved; store.events is empty")
	}
	e := h.store.events[0]
	if e.IsSensitive {
		t.Errorf("non-sensitive path should not be flagged")
	}
	if strings.Contains(string(e.Payload), "AKIAZZZZZZZZZZZZZZZZ") {
		t.Errorf("secret survived in payload after redaction: %s", e.Payload)
	}
	if !strings.Contains(string(e.Payload), "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker; got %s", e.Payload)
	}
}

// --- Logging level ---

func TestFunnel_MinimalLoggingLevel_StripsContent(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.Logging.Level = LoggingLevelMinimal
	h := newFunnelHarness(t, cfg, &model.LockEntry{Name: "foo"})
	raw := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "command_exec",
		"payload": map[string]any{
			"path":    filepath.Join(h.wts["foo"], "f"),
			"command": "ls",
			"stdout":  "file1\nfile2",
			"stderr":  "",
		},
	})
	if err := h.funnel.Ingest(context.Background(), "x", raw); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) == 0 {
		t.Fatalf("event was not saved; store.events is empty")
	}
	e := h.store.events[0]
	if strings.Contains(string(e.Payload), "file1") {
		t.Errorf("minimal level should have stripped stdout: %s", e.Payload)
	}
}

// --- Session end sets EndedAt ---

func TestFunnel_SessionEnd_SetsEndedAt(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	raw := mustJSON(t, map[string]any{
		"agent_name":        "claude",
		"agent_session_id":  "s",
		"action_type":       "session_end",
		"working_directory": h.wts["foo"],
	})
	if err := h.funnel.Ingest(context.Background(), "SessionEnd", raw); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for _, s := range h.store.sessions {
		if s.EndedAt == nil {
			t.Errorf("expected EndedAt to be set")
		}
	}
}

// --- Store failure bubbles up ---

func TestFunnel_SaveEventError_Bubbles(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	h.store.failSave = fmt.Errorf("disk full")
	raw := mustJSON(t, map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "file_read",
		"payload":          map[string]any{"path": filepath.Join(h.wts["foo"], "x")},
	})
	err := h.funnel.Ingest(context.Background(), "x", raw)
	if err == nil {
		t.Errorf("expected error to propagate")
	}
}

func TestFunnel_AuditWriteFailure_BubblesFromHookError(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	h.store.failAudit = fmt.Errorf("audit table gone")
	err := h.funnel.Ingest(context.Background(), "x", []byte("not json"))
	if err == nil {
		t.Errorf("expected audit failure to bubble")
	}
}

// --- NewFunnel validation ---

func TestNewFunnel_RequiresDeps(t *testing.T) {
	cases := []struct {
		name string
		deps FunnelDeps
	}{
		{"missing config", FunnelDeps{Store: newMemStore(), Resolver: stubResolver{}, Privacy: stubChecker{}}},
		{"missing store", FunnelDeps{Config: &config.Config{}, Resolver: stubResolver{}, Privacy: stubChecker{}}},
		{"missing resolver", FunnelDeps{Config: &config.Config{}, Store: newMemStore(), Privacy: stubChecker{}}},
		{"missing privacy", FunnelDeps{Config: &config.Config{}, Store: newMemStore(), Resolver: stubResolver{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewFunnel(tc.deps)
			if err == nil {
				t.Errorf("expected error")
			}
		})
	}
}

func TestNewFunnel_ClockDefaultsToNow(t *testing.T) {
	fn, err := NewFunnel(FunnelDeps{
		Config:   &config.Config{},
		Store:    newMemStore(),
		Resolver: stubResolver{},
		Privacy:  stubChecker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fn.deps.Clock == nil {
		t.Errorf("expected default Clock")
	}
}

// --- Concurrency ---

func TestFunnel_ConcurrentIngests(t *testing.T) {
	h := newFunnelHarness(t, nil, &model.LockEntry{Name: "foo"})
	path := filepath.Join(h.wts["foo"], "SKILL.md")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw := mustJSON(t, map[string]any{
				"agent_name":       "claude",
				"agent_session_id": fmt.Sprintf("s-%d", i),
				"action_type":      "file_read",
				"payload":          map[string]any{"path": path},
			})
			_ = h.funnel.Ingest(context.Background(), "x", raw)
		}(i)
	}
	wg.Wait()

	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.events) != 20 {
		t.Errorf("expected 20 events; got %d", len(h.store.events))
	}
}

// --- Stubs for NewFunnel validation tests ---

type stubResolver struct{}

func (stubResolver) Attribute(*Event) (Attribution, bool)    { return Attribution{}, false }
func (stubResolver) AttributeByName(name string) Attribution { return Attribution{Name: name} }

type stubChecker struct{}

func (stubChecker) Evaluate(privacy.Event) privacy.Decision { return privacy.Decision{} }

// --- small helpers ---

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
