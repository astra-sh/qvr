package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"
)

func seedScoredSession(t *testing.T, st Store, sid uuid.UUID, sourceRef, chash string) {
	t.Helper()
	span := &SpanRow{
		SpanID: "s_" + sid.String()[:8], TraceID: "tr", SessionID: sid, AgentName: "claude",
		Kind: "SKILL", Name: "load", StartMs: 100, EndMs: 100,
		Attributes: `{"skill.name":"slugify","skill.content_hash":"` + chash + `","skill.activation":"tool","qvr.outcome":"success"}`,
	}
	meta := &SessionMetaRow{
		SessionID: sid, AgentName: "claude", SourceSessionID: sourceRef,
		StartedMs: 100, EndedMs: 100, Skills: []string{"slugify"},
	}
	if err := st.ReplaceSessionDerivation(context.Background(), meta, []*SpanRow{span}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestSessionScore_RollsIntoCohort_CaseInsensitive is the slot end to end: a
// grade written for the agent-native session id joins to a session whose
// source_session_id was stored UPPERCASE (as claude does) and folds into the
// content cohort the session's spans ran. Without the LOWER() on both sides — the
// exact mismatch on the live DB — the grade would silently orphan.
func TestSessionScore_RollsIntoCohort_CaseInsensitive(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()
	upperRef := "DCE3AAAA-BBBB-CCCC-DDDD-EEEEEEEE701F" // claude stores it verbatim/uppercase

	seedScoredSession(t, st, sid, upperRef, "sha256:e6c6badeff")

	// Grader passes a capitalized agent and a LOWERCASE id (e.g. echoed from
	// --output-format json) — both must normalize to match the stored session.
	if err := st.PutScore(ctx, "Claude", strings.ToLower(upperRef), "slugify", "accuracy", 1.0, "exact"); err != nil {
		t.Fatalf("put score: %v", err)
	}

	cohorts, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "slugify", Metric: "accuracy"})
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(cohorts) != 1 {
		t.Fatalf("want 1 cohort, got %d", len(cohorts))
	}
	c := cohorts[0]
	if c.Graded != 1 || c.MeanScore == nil || *c.MeanScore != 1.0 {
		t.Errorf("grade must roll into the cohort despite case mismatch: graded=%d mean=%v", c.Graded, c.MeanScore)
	}
}

// seedMultiSkillSession seeds one session that loaded two distinct skills (each
// its own SKILL span + content hash) — the shape that exposes the double-count
// bug: one session-grain grade would otherwise count under both skills' cohorts.
func seedMultiSkillSession(t *testing.T, st Store, sid uuid.UUID, sourceRef string, skills [][2]string) {
	t.Helper()
	spans := make([]*SpanRow, 0, len(skills))
	names := make([]string, 0, len(skills))
	for i, sk := range skills {
		name, chash := sk[0], sk[1]
		spans = append(spans, &SpanRow{
			SpanID: "s_" + sid.String()[:6] + string(rune('a'+i)), TraceID: "tr", SessionID: sid, AgentName: "claude",
			Kind: "SKILL", Name: "load", StartMs: 100, EndMs: 100,
			Attributes: `{"skill.name":"` + name + `","skill.content_hash":"` + chash + `","skill.activation":"tool","qvr.outcome":"success"}`,
		})
		names = append(names, name)
	}
	meta := &SessionMetaRow{
		SessionID: sid, AgentName: "claude", SourceSessionID: sourceRef,
		StartedMs: 100, EndedMs: 100, Skills: names,
	}
	if err := st.ReplaceSessionDerivation(context.Background(), meta, spans); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestSessionScore_SkillScoped_NoDoubleCount is the (skill, session) fix: a
// session that loaded two skills, graded for ONE of them, must fold into that
// skill's cohort only — the other skill's cohort stays ungraded. Before the skill
// key, the single grade joined every cohort the session touched and inflated both.
func TestSessionScore_SkillScoped_NoDoubleCount(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()
	seedMultiSkillSession(t, st, sid, "REF-MULTI", [][2]string{
		{"triage", "sha256:triagehash"},
		{"slugify", "sha256:slugifyhash"},
	})

	// Grade the session for triage only.
	if err := st.PutScore(ctx, "claude", "ref-multi", "triage", "accuracy", 1.0, "exact"); err != nil {
		t.Fatalf("put score: %v", err)
	}

	triage, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "triage", Metric: "accuracy"})
	if err != nil {
		t.Fatalf("rollup triage: %v", err)
	}
	if len(triage) != 1 || triage[0].Graded != 1 || triage[0].MeanScore == nil || *triage[0].MeanScore != 1.0 {
		t.Fatalf("triage cohort must carry the grade: %+v", triage[0])
	}

	// The other skill the same session loaded must NOT inherit the grade.
	slugify, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "slugify", Metric: "accuracy"})
	if err != nil {
		t.Fatalf("rollup slugify: %v", err)
	}
	if len(slugify) != 1 || slugify[0].Graded != 0 || slugify[0].MeanScore != nil {
		t.Errorf("a grade scoped to triage must not count under slugify: %+v", slugify[0])
	}
}

// TestSessionSkillLoaded backs annotate's guard: an undiscovered run is known=false
// (no opinion), a discovered run reports whether it loaded the named skill.
func TestSessionSkillLoaded(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()
	seedScoredSession(t, st, sid, "REF-GUARD", "sha256:guardhash") // loads "slugify"

	cases := []struct {
		name                string
		ref, skill          string
		wantKnown, wantLoad bool
	}{
		{"discovered + loaded", "ref-guard", "slugify", true, true},
		{"discovered, skill not loaded", "ref-guard", "triage", true, false},
		{"undiscovered session", "ref-missing", "slugify", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			known, loaded, err := st.SessionSkillLoaded(ctx, "claude", tc.ref, tc.skill)
			if err != nil {
				t.Fatalf("session skill loaded: %v", err)
			}
			if known != tc.wantKnown || loaded != tc.wantLoad {
				t.Errorf("known=%v loaded=%v; want known=%v loaded=%v", known, loaded, tc.wantKnown, tc.wantLoad)
			}
		})
	}
}

// TestSkillScoreMetrics backs compare's metric-mismatch hint: it lists the
// distinct metrics a skill carries grades under (sorted), scoped to that skill so
// another skill's metrics never leak in, and stays empty for an ungraded skill.
func TestSkillScoreMetrics(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.PutScore(ctx, "claude", "ref-a", "slugify", "rubric", 1.0, "llm"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScore(ctx, "claude", "ref-b", "slugify", "exact", 1.0, "exact"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScore(ctx, "claude", "ref-c", "triage", "score", 1.0, "exact"); err != nil {
		t.Fatal(err) // a different skill must not bleed into slugify's metrics
	}

	got, err := st.SkillScoreMetrics(ctx, "Slugify") // case-insensitive on skill
	if err != nil {
		t.Fatalf("skill score metrics: %v", err)
	}
	if want := []string{"exact", "rubric"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("want sorted %v scoped to slugify, got %v", want, got)
	}

	none, err := st.SkillScoreMetrics(ctx, "never-graded")
	if err != nil {
		t.Fatalf("skill score metrics: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("an ungraded skill must report no metrics, got %v", none)
	}
}

// TestScoresForSessions_JoinsByNormalizedKey is the read-back the session detail
// and list surfaces rely on: a grade written against a claude session whose
// source id is stored UPPERCASE joins back through (agent, LOWER(source_session_id)),
// every (skill, metric) is returned, an ungraded session is simply absent, and an
// empty id set short-circuits to an empty map.
func TestScoresForSessions_JoinsByNormalizedKey(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	graded, ungraded := uuid.New(), uuid.New()
	upperRef := "AAAA1111-BBBB-CCCC-DDDD-EEEEEEEE9999" // claude stores it uppercase
	seedScoredSession(t, st, graded, upperRef, "sha256:body")
	seedScoredSession(t, st, ungraded, "REF-UNGRADED", "sha256:body")

	// Two metrics on the one graded session; grader passes a lowercase id.
	if err := st.PutScore(ctx, "claude", strings.ToLower(upperRef), "slugify", "score", 1.0, "exact"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScore(ctx, "claude", strings.ToLower(upperRef), "slugify", "rubric", 0.5, "llm"); err != nil {
		t.Fatal(err)
	}

	got, err := st.ScoresForSessions(ctx, []string{graded.String(), ungraded.String()})
	if err != nil {
		t.Fatalf("scores for sessions: %v", err)
	}
	if _, ok := got[ungraded.String()]; ok {
		t.Errorf("an ungraded session must be absent from the map, got %v", got[ungraded.String()])
	}
	rows := got[graded.String()]
	if len(rows) != 2 {
		t.Fatalf("want 2 grades joined despite uppercase id, got %d: %+v", len(rows), rows)
	}
	// Ordered by (skill, metric): "rubric" sorts before "score".
	if rows[0].Metric != "rubric" || rows[0].Value != 0.5 || rows[0].Grader != "llm" {
		t.Errorf("first grade = %+v, want rubric/0.5/llm", rows[0])
	}
	if rows[1].Metric != "score" || rows[1].Value != 1.0 || rows[1].Skill != "slugify" {
		t.Errorf("second grade = %+v, want score/1.0/slugify", rows[1])
	}
	if rows[1].ScoredAt == 0 {
		t.Errorf("scoredAt must carry the write time, got 0")
	}

	empty, err := st.ScoresForSessions(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("empty id set must short-circuit to an empty map, got %v err=%v", empty, err)
	}
}

// TestSkillVersionRollup_FoldsScores is the lineage-projection score: two runs of
// one skill at two pinned versions, each graded, must surface their own MeanScore
// on the (ref, commit) version row — the data behind the skill page's per-version
// SCORE chip. A 0.0 grade stays a real score (MeanScore non-nil), never an absent.
func TestSkillVersionRollup_FoldsScores(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seedVersionRun := func(sid uuid.UUID, ref, sourceRef string) {
		span := &SpanRow{
			SpanID: "s_" + sid.String()[:8], TraceID: "tr", SessionID: sid, AgentName: "claude",
			Kind: "SKILL", Name: "load", StartMs: 100, EndMs: 100,
			Attributes: `{"skill.name":"slugify","skill.version":"` + ref +
				`","skill.commit":"c` + ref + `","skill.activation":"tool"}`,
		}
		meta := &SessionMetaRow{
			SessionID: sid, AgentName: "claude", SourceSessionID: sourceRef,
			StartedMs: 100, EndedMs: 100, Skills: []string{"slugify"},
		}
		if err := st.ReplaceSessionDerivation(ctx, meta, []*SpanRow{span}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	a, b := uuid.New(), uuid.New()
	seedVersionRun(a, "v1", "REF-A")
	seedVersionRun(b, "v2", "REF-B")
	if err := st.PutScore(ctx, "claude", "ref-a", "slugify", "score", 1.0, "exact"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScore(ctx, "claude", "ref-b", "slugify", "score", 0.0, "exact"); err != nil {
		t.Fatal(err)
	}

	rows, err := st.SkillVersionRollup(ctx, &MetricsFilter{Skill: "slugify"})
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	byRef := map[string]*SkillVersionUsage{}
	for _, r := range rows {
		byRef[r.Ref] = r
	}
	if v := byRef["v1"]; v == nil || v.Graded != 1 || v.MeanScore == nil || *v.MeanScore != 1.0 {
		t.Errorf("v1 = %+v, want graded 1 mean 1.0", v)
	}
	if v := byRef["v2"]; v == nil || v.Graded != 1 || v.MeanScore == nil || *v.MeanScore != 0.0 {
		t.Errorf("v2 = %+v, want graded 1 mean 0.0 (0 is a real grade, not absent)", v)
	}
}

func tokPtr(v int64) *int64 { return &v }

// seedAgentRun seeds one session of `skill` at content version `chash`, run by
// `agent`, with the given session token totals (nil = no usage reported).
func seedAgentRun(t *testing.T, st Store, sid uuid.UUID, agent, sourceRef, skill, chash string, tin, tout *int64) {
	t.Helper()
	span := &SpanRow{
		SpanID: "s_" + sid.String()[:8], TraceID: "tr", SessionID: sid, AgentName: agent,
		Kind: "SKILL", Name: "load", StartMs: 100, EndMs: 100,
		Attributes: `{"skill.name":"` + skill + `","skill.content_hash":"` + chash + `","skill.activation":"tool","qvr.outcome":"success"}`,
	}
	meta := &SessionMetaRow{
		SessionID: sid, AgentName: agent, SourceSessionID: sourceRef,
		StartedMs: 100, EndedMs: 100, Skills: []string{skill}, TokensIn: tin, TokensOut: tout,
	}
	if err := st.ReplaceSessionDerivation(context.Background(), meta, []*SpanRow{span}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestSkillContentRollup_ByAgent_SplitsScoresAndTokens is the {version × agent}
// matrix: one skill version run by three agents collapses to a single aggregate
// row by default, and splits into one row per agent under ByAgent — each cell
// carrying its OWN graded score and its OWN token cost (and a token-less agent
// keeping nil sides, never a fabricated 0).
func TestSkillContentRollup_ByAgent_SplitsScoresAndTokens(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	cl, co, he := uuid.New(), uuid.New(), uuid.New()
	seedAgentRun(t, st, cl, "claude", "REF-CL", "triage", "sha256:v1", tokPtr(100), tokPtr(40))
	seedAgentRun(t, st, co, "codex", "REF-CO", "triage", "sha256:v1", tokPtr(60), tokPtr(10))
	seedAgentRun(t, st, he, "hermes", "REF-HE", "triage", "sha256:v1", nil, nil) // token-less
	for _, p := range []struct {
		agent, ref string
		score      float64
	}{{"claude", "ref-cl", 1.0}, {"codex", "ref-co", 0.0}, {"hermes", "ref-he", 1.0}} {
		if err := st.PutScore(ctx, p.agent, p.ref, "triage", "score", p.score, "exact"); err != nil {
			t.Fatalf("put score %s: %v", p.agent, err)
		}
	}

	// Collapsed (default): one cohort, scores + tokens merged across agents.
	all, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "triage"})
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("collapsed: want 1 cohort, got %d", len(all))
	}
	c := all[0]
	if c.Agent != "" {
		t.Errorf("collapsed cohort must not carry an agent, got %q", c.Agent)
	}
	if c.Graded != 3 {
		t.Errorf("collapsed graded = %d, want 3", c.Graded)
	}
	assertCohortCell(t, "collapsed", c, fptr(2.0/3.0), tokPtr(160), tokPtr(50))

	// By agent: one cell per agent, each its own score + cost.
	rows, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "triage", ByAgent: true})
	if err != nil {
		t.Fatalf("rollup by-agent: %v", err)
	}
	byAgent := map[string]*SkillContentCohort{}
	for _, r := range rows {
		byAgent[r.Agent] = r
	}
	if len(byAgent) != 3 {
		t.Fatalf("by-agent: want 3 cells, got %d", len(rows))
	}
	assertCohortCell(t, "claude", byAgent["claude"], fptr(1.0), tokPtr(100), tokPtr(40))
	assertCohortCell(t, "codex", byAgent["codex"], fptr(0.0), tokPtr(60), tokPtr(10))
	// Token-less agent: nil sides preserved (never a fabricated 0).
	assertCohortCell(t, "hermes", byAgent["hermes"], fptr(1.0), nil, nil)
}

func fptr(v float64) *float64 { return &v }

// eqFloatPtr / eqIntPtr compare nullable values where nil means "absent" — so a
// fabricated 0 never matches an expected nil.
func eqFloatPtr(got, want *float64) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

func eqIntPtr(got, want *int64) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

// assertCohortCell checks a cohort cell's graded mean and token sides, treating
// nil expectations as "must be absent".
func assertCohortCell(t *testing.T, name string, c *SkillContentCohort, mean *float64, in, out *int64) {
	t.Helper()
	if c == nil {
		t.Fatalf("%s: missing cohort cell", name)
	}
	if !eqFloatPtr(c.MeanScore, mean) {
		t.Errorf("%s mean = %v, want %v", name, c.MeanScore, mean)
	}
	if !eqIntPtr(c.InputTokens, in) {
		t.Errorf("%s input tokens = %v, want %v", name, c.InputTokens, in)
	}
	if !eqIntPtr(c.OutputTokens, out) {
		t.Errorf("%s output tokens = %v, want %v", name, c.OutputTokens, out)
	}
}

// TestSessionScore_MeanAndHonestNil: two graded runs of one version average to a
// pass-rate; a metric with no score for the cohort stays nil (never 0.0, since 0
// is a real failing grade).
func TestSessionScore_MeanAndHonestNil(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	s1, s2 := uuid.New(), uuid.New()
	seedScoredSession(t, st, s1, "REF-1", "sha256:samebody")
	seedScoredSession(t, st, s2, "REF-2", "sha256:samebody") // same content cohort

	if err := st.PutScore(ctx, "claude", "ref-1", "slugify", "accuracy", 1.0, "exact"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScore(ctx, "claude", "ref-2", "slugify", "accuracy", 0.0, "exact"); err != nil {
		t.Fatal(err)
	}

	got, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "slugify", Metric: "accuracy"})
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(got) != 1 || got[0].Graded != 2 || got[0].MeanScore == nil || *got[0].MeanScore != 0.5 {
		t.Fatalf("two graded runs must average to 0.5 over 2: %+v", got[0])
	}

	// A different metric has no scores → honest nil, not 0.0.
	other, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "slugify", Metric: "latency"})
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(other) != 1 || other[0].MeanScore != nil || other[0].Graded != 0 {
		t.Errorf("an ungraded metric must be nil, never 0.0: %+v", other[0])
	}
}
