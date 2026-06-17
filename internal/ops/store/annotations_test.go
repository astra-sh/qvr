package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestAnnotations_RoundTrip pins the write/read path: a verdict reads back with
// its fields, and filters scope by session and skill.
func TestAnnotations_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	if err := st.PutAnnotation(ctx, &AnnotationRow{
		SessionID: sid, Skill: "triage-issue", Outcome: "bad",
		Note: "ambiguous: needs a setting", Author: "rakshith",
	}); err != nil {
		t.Fatalf("put annotation: %v", err)
	}

	got, err := st.ListAnnotations(ctx, &AnnotationFilter{SessionID: &sid})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 annotation, got %d", len(got))
	}
	a := got[0]
	if a.Skill != "triage-issue" || a.Outcome != "bad" ||
		a.Note != "ambiguous: needs a setting" || a.Author != "rakshith" {
		t.Errorf("annotation did not round-trip: %+v", a)
	}
	if a.CreatedAt.IsZero() {
		t.Error("created_at was not stamped")
	}

	// Skill filter matches the per-skill verdict.
	bySkill, _ := st.ListAnnotations(ctx, &AnnotationFilter{Skill: "triage-issue"})
	if len(bySkill) != 1 {
		t.Errorf("skill filter: want 1, got %d", len(bySkill))
	}
	none, _ := st.ListAnnotations(ctx, &AnnotationFilter{Skill: "other"})
	if len(none) != 0 {
		t.Errorf("skill filter (other): want 0, got %d", len(none))
	}
}

// TestAnnotations_RequiresOutcome pins the validation guard.
func TestAnnotations_RequiresOutcome(t *testing.T) {
	st := openTestStore(t)
	if err := st.PutAnnotation(context.Background(), &AnnotationRow{SessionID: uuid.New()}); err == nil {
		t.Error("expected an error for a verdict with no outcome")
	}
}

// TestAnnotations_SurviveRederive proves the whole point of a separate table:
// re-deriving a session (which replaces spans + session_meta) does NOT touch
// the human verdict.
func TestAnnotations_SurviveRederive(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	if err := st.ReplaceSessionDerivation(ctx, metaFor(sid, "claude-code", 1000, "triage-issue"), nil); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if err := st.PutAnnotation(ctx, &AnnotationRow{SessionID: sid, Skill: "triage-issue", Outcome: "bad"}); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	// Re-derive (the rederive path uses this same write).
	if err := st.ReplaceSessionDerivation(ctx, metaFor(sid, "claude-code", 1000, "triage-issue"), nil); err != nil {
		t.Fatalf("re-derive: %v", err)
	}

	got, _ := st.ListAnnotations(ctx, &AnnotationFilter{SessionID: &sid})
	if len(got) != 1 {
		t.Fatalf("annotation did not survive rederive: got %d", len(got))
	}
}

// TestAnnotations_ClearedOnDeleteSession proves a hard session purge removes
// its verdicts (nothing left to annotate).
func TestAnnotations_ClearedOnDeleteSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	if err := st.ReplaceSessionDerivation(ctx, metaFor(sid, "codex", 1000, "tdd"), nil); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if err := st.PutAnnotation(ctx, &AnnotationRow{SessionID: sid, Outcome: "good"}); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	if _, err := st.DeleteSession(ctx, sid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := st.ListAnnotations(ctx, &AnnotationFilter{SessionID: &sid})
	if len(got) != 0 {
		t.Errorf("annotations survived DeleteSession: %d", len(got))
	}
}
