package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func metaFor(sid uuid.UUID, agent string, startedMs int64, skills ...string) *SessionMetaRow {
	return &SessionMetaRow{
		SessionID:       sid,
		AgentName:       agent,
		SourceSessionID: "src-" + sid.String()[:8],
		SourcePath:      "/store/" + sid.String()[:8] + ".jsonl",
		WorkingDir:      "/tmp/proj",
		Model:           "model-x",
		Title:           "do the thing",
		StartedMs:       startedMs,
		EndedMs:         startedMs + 1000,
		Turns:           2,
		Tools:           3,
		Skills:          skills,
		DeriverVersion:  1,
	}
}

// TestReplaceSessionDerivation_RoundTrip pins the unified write path: meta +
// spans land atomically and read back exactly, and a re-derivation replaces
// (not accretes) both.
func TestReplaceSessionDerivation_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	meta := metaFor(sid, "claude-code", 1000, "code-review")
	spans := []*SpanRow{
		{SpanID: "s1", TraceID: "t", SessionID: sid, AgentName: "claude-code", Kind: "LLM", Name: "turn", StartMs: 1000, EndMs: 2000},
		{SpanID: "s2", TraceID: "t", SessionID: sid, AgentName: "claude-code", Kind: "SKILL", Name: "code-review", StartMs: 1100, EndMs: 1200},
	}
	if err := st.ReplaceSessionDerivation(ctx, meta, spans); err != nil {
		t.Fatalf("replace derivation: %v", err)
	}

	assertMetaRoundTrip(t, st, sid)

	gotSpans, err := st.QuerySpans(ctx, &SpanFilter{SessionID: &sid})
	if err != nil {
		t.Fatalf("query spans: %v", err)
	}
	if len(gotSpans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(gotSpans))
	}

	// Re-derive with fewer spans and no skills: both projections must shrink.
	meta2 := metaFor(sid, "claude-code", 1000)
	meta2.Turns = 1
	if err := st.ReplaceSessionDerivation(ctx, meta2, spans[:1]); err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	got2, _ := st.GetSessionMeta(ctx, sid)
	if got2 == nil || got2.Turns != 1 || len(got2.Skills) != 0 {
		t.Errorf("re-derivation did not replace meta: %+v", got2)
	}
	gotSpans2, _ := st.QuerySpans(ctx, &SpanFilter{SessionID: &sid})
	if len(gotSpans2) != 1 {
		t.Errorf("re-derivation did not replace spans: %d", len(gotSpans2))
	}
}

// assertMetaRoundTrip checks the written meta reads back field-for-field.
func assertMetaRoundTrip(t *testing.T, st Store, sid uuid.UUID) {
	t.Helper()
	got, err := st.GetSessionMeta(context.Background(), sid)
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if got == nil {
		t.Fatal("meta not found after write")
	}
	if got.AgentName != "claude-code" || got.Title != "do the thing" ||
		got.Model != "model-x" || got.StartedMs != 1000 || got.EndedMs != 2000 ||
		got.Turns != 2 || got.Tools != 3 {
		t.Errorf("meta did not round-trip: %+v", got)
	}
	if len(got.Skills) != 1 || got.Skills[0] != "code-review" {
		t.Errorf("skills did not round-trip: %v", got.Skills)
	}
	if got.SourceSessionID == "" || got.SourcePath == "" || got.WorkingDir != "/tmp/proj" {
		t.Errorf("source identity did not round-trip: %+v", got)
	}
}

// TestListSessionMeta_Filters exercises agent/dir/skill/window scoping over
// the unified read model.
func TestListSessionMeta_Filters(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	a, b, c := uuid.New(), uuid.New(), uuid.New()
	mustDerive := func(m *SessionMetaRow) {
		t.Helper()
		if err := st.ReplaceSessionDerivation(ctx, m, nil); err != nil {
			t.Fatalf("derive %s: %v", m.SessionID, err)
		}
	}
	mustDerive(metaFor(a, "claude-code", 1000, "code-review"))
	mustDerive(metaFor(b, "codex", 2000, "tdd", "code-review"))
	skillless := metaFor(c, "claude-code", 3000)
	skillless.WorkingDir = "/tmp/other"
	mustDerive(skillless)

	cases := []struct {
		name   string
		filter *SessionMetaFilter
		want   []uuid.UUID // newest-first
	}{
		{"all newest-first", nil, []uuid.UUID{c, b, a}},
		{"agent", &SessionMetaFilter{Agent: "codex"}, []uuid.UUID{b}},
		{"dir", &SessionMetaFilter{Dirs: []string{"/tmp/other"}}, []uuid.UUID{c}},
		{"skill", &SessionMetaFilter{Skill: "code-review"}, []uuid.UUID{b, a}},
		{"skills multi single", &SessionMetaFilter{Skills: []string{"tdd"}}, []uuid.UUID{b}},
		{"skills multi union", &SessionMetaFilter{Skills: []string{"tdd", "code-review"}}, []uuid.UUID{b, a}},
		{"skills + scalar merge", &SessionMetaFilter{Skills: []string{"tdd"}, Skill: "code-review"}, []uuid.UUID{b, a}},
		{"skills-only", &SessionMetaFilter{SkillsOnly: true}, []uuid.UUID{b, a}},
		{"since", &SessionMetaFilter{Since: msPtr(1500)}, []uuid.UUID{c, b}},
		{"until", &SessionMetaFilter{Until: msPtr(1500)}, []uuid.UUID{a}},
		{"limit", &SessionMetaFilter{Limit: 1}, []uuid.UUID{c}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.ListSessionMeta(ctx, tc.filter)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("want %d rows, got %d", len(tc.want), len(got))
			}
			for i, w := range tc.want {
				if got[i].SessionID != w {
					t.Errorf("row %d: want %s, got %s", i, w, got[i].SessionID)
				}
			}
		})
	}
}

// TestSessionMetaTokens_RoundTrip pins the nullable token columns: nil stays
// nil (no usage reported → n/a), a genuine 0 stays 0 — never conflated.
func TestSessionMetaTokens_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	noUsage, zero, real := uuid.New(), uuid.New(), uuid.New()
	mustDerive := func(m *SessionMetaRow) {
		t.Helper()
		if err := st.ReplaceSessionDerivation(ctx, m, nil); err != nil {
			t.Fatalf("derive %s: %v", m.SessionID, err)
		}
	}
	mustDerive(metaFor(noUsage, "cursor", 1000, "code-review"))
	z := metaFor(zero, "claude-code", 2000, "code-review")
	z.TokensIn, z.TokensOut = i64(0), i64(0)
	mustDerive(z)
	r := metaFor(real, "claude-code", 3000, "code-review")
	r.TokensIn, r.TokensOut = i64(462522), i64(4246)
	mustDerive(r)

	assertTokenPair(t, st, noUsage, nil, nil)
	assertTokenPair(t, st, zero, i64(0), i64(0))
	assertTokenPair(t, st, real, i64(462522), i64(4246))

	// SortByTokens: token totals desc, NULLs (n/a) last — a token-less agent
	// must never outrank a measured one, and a real 0 ranks above n/a.
	rows, err := st.ListSessionMeta(ctx, &SessionMetaFilter{SortByTokens: true})
	if err != nil {
		t.Fatalf("list sorted: %v", err)
	}
	want := []uuid.UUID{real, zero, noUsage}
	if len(rows) != len(want) {
		t.Fatalf("want %d rows, got %d", len(want), len(rows))
	}
	for i, w := range want {
		if rows[i].SessionID != w {
			t.Errorf("sorted row %d: want %s, got %s", i, w, rows[i].SessionID)
		}
	}
}

// assertTokenPair checks one session's persisted token totals, nil meaning
// "no usage reported" — distinct from a pointer to 0.
func assertTokenPair(t *testing.T, st Store, sid uuid.UUID, wantIn, wantOut *int64) {
	t.Helper()
	got, err := st.GetSessionMeta(context.Background(), sid)
	if err != nil || got == nil {
		t.Fatalf("get %s: %v %v", sid, got, err)
	}
	if !i64Equal(got.TokensIn, wantIn) || !i64Equal(got.TokensOut, wantOut) {
		t.Errorf("session %s tokens = %v/%v, want %v/%v", sid, got.TokensIn, got.TokensOut, wantIn, wantOut)
	}
}

func i64Equal(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// TestSessionMetaOutcome_RoundTrip pins the nullable outcome column: a set
// verdict reads back, and "" (no span carried an outcome) reads back as "".
func TestSessionMetaOutcome_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	withOutcome, unknown := uuid.New(), uuid.New()
	o := metaFor(withOutcome, "claude-code", 1000, "code-review")
	o.Outcome = "failure"
	if err := st.ReplaceSessionDerivation(ctx, o, nil); err != nil {
		t.Fatalf("derive with outcome: %v", err)
	}
	if err := st.ReplaceSessionDerivation(ctx, metaFor(unknown, "codex", 2000, "tdd"), nil); err != nil {
		t.Fatalf("derive without outcome: %v", err)
	}

	got, err := st.GetSessionMeta(ctx, withOutcome)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Outcome != "failure" {
		t.Errorf("outcome = %q, want failure", got.Outcome)
	}
	got2, _ := st.GetSessionMeta(ctx, unknown)
	if got2 == nil || got2.Outcome != "" {
		t.Errorf("outcome = %q, want \"\" (unknown)", got2.Outcome)
	}
}

// skillSpanRow builds a minimal SKILL span carrying skill.name + skill.version
// attributes — the per-span tagging the version rollup/filter read from.
func skillSpanRow(sid uuid.UUID, spanID, version string) *SpanRow {
	return &SpanRow{
		SpanID:     spanID,
		TraceID:    "tr-" + spanID,
		SessionID:  sid,
		AgentName:  "claude-code",
		Kind:       "SKILL",
		Name:       "code-review",
		StartMs:    1,
		EndMs:      2,
		Attributes: `{"skill.name":"code-review","skill.version":"` + version + `"}`,
	}
}

// TestListSessionMeta_VersionFilter pins the server-side VERSION filter: it
// scopes the list to sessions whose SKILL spans carry the exact skill.version —
// the same per-span tag the read-side rollup (SkillVersionsForSessions) reads,
// so there is no denormalized column to keep in sync. Driven by real SKILL
// spans, end-to-end through ReplaceSessionDerivation → ListSessionMeta.
func TestListSessionMeta_VersionFilter(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	v1, v2 := uuid.New(), uuid.New()
	if err := st.ReplaceSessionDerivation(ctx, metaFor(v1, "claude-code", 1000, "code-review"),
		[]*SpanRow{skillSpanRow(v1, "s1", "v0.1.0")}); err != nil {
		t.Fatalf("derive v1: %v", err)
	}
	if err := st.ReplaceSessionDerivation(ctx, metaFor(v2, "claude-code", 2000, "code-review"),
		[]*SpanRow{skillSpanRow(v2, "s2", "v0.2.0")}); err != nil {
		t.Fatalf("derive v2: %v", err)
	}
	// A session with no versioned SKILL span must be excluded by a version filter.
	noVer := uuid.New()
	if err := st.ReplaceSessionDerivation(ctx, metaFor(noVer, "codex", 3000, "tdd"), nil); err != nil {
		t.Fatalf("derive noVer: %v", err)
	}

	rows, err := st.ListSessionMeta(ctx, &SessionMetaFilter{Version: "v0.2.0"})
	if err != nil {
		t.Fatalf("list by version: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != v2 {
		t.Errorf("version filter = %v, want only the v0.2.0 session", rows)
	}
}

// TestGetSessionMeta_AbsentIsNil pins the no-row contract: nil, not an error.
func TestGetSessionMeta_AbsentIsNil(t *testing.T) {
	st := openTestStore(t)
	got, err := st.GetSessionMeta(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("get absent meta: %v", err)
	}
	if got != nil {
		t.Errorf("want nil for absent session, got %+v", got)
	}
}

// TestDeleteSession_RemovesMeta proves DeleteSession clears the unified row
// alongside raw/spans/cursor.
func TestDeleteSession_RemovesMeta(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()
	if err := st.ReplaceSessionDerivation(ctx, metaFor(sid, "codex", 1000, "tdd"), nil); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if _, err := st.DeleteSession(ctx, sid); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	got, err := st.GetSessionMeta(ctx, sid)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("session_meta row survived DeleteSession: %+v", got)
	}
}

func msPtr(ms int64) *time.Time {
	t := time.UnixMilli(ms).UTC()
	return &t
}
