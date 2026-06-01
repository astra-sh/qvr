package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/ops"
)

func TestBackfillSkill_StampsOnlyPending(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sess := mkSession(t, "claude")
	if err := s.UpsertSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	// Two provisional events + one already attributed to a different skill.
	for _, sk := range []string{ops.SkillPending, ops.SkillPending, "other"} {
		if err := s.SaveEvent(ctx, mkEvent(t, sess, ops.ActionCommandExec, sk)); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.BackfillSkill(ctx, sess.ID, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("backfilled %d rows; want 2", n)
	}

	events, err := s.QueryEvents(ctx, &EventFilter{SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	var foo, other int
	for _, e := range events {
		switch e.SkillName {
		case "foo":
			foo++
		case "other":
			other++
		case ops.SkillPending:
			t.Errorf("pending row survived back-fill")
		}
	}
	if foo != 2 || other != 1 {
		t.Errorf("foo=%d other=%d; want 2 and 1 (already-attributed row untouched)", foo, other)
	}
}

func TestDeleteSession_RemovesSessionAndEvents(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sess := mkSession(t, "claude")
	if err := s.UpsertSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.SaveEvent(ctx, mkEvent(t, sess, ops.ActionFileRead, ops.SkillPending)); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.DeleteSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("deleted %d events; want 3", n)
	}
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("session survived delete")
	}
	events, _ := s.QueryEvents(ctx, &EventFilter{SessionID: &sess.ID})
	if len(events) != 0 {
		t.Errorf("expected events removed; got %d", len(events))
	}
}

func TestDeleteSkilllessSessions_SweepsOldOrphansOnly(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Old, skill-less orphan — should be swept.
	orphan := ops.NewSession("claude", "orphan", now.Add(-48*time.Hour))
	if err := s.UpsertSession(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveEvent(ctx, mkEvent(t, orphan, ops.ActionCommandExec, ops.SkillPending)); err != nil {
		t.Fatal(err)
	}

	// Old, skill-bearing — must be retained.
	kept := ops.NewSession("claude", "kept", now.Add(-48*time.Hour))
	kept.AddSkillTouched("foo")
	if err := s.UpsertSession(ctx, kept); err != nil {
		t.Fatal(err)
	}

	// Recent skill-less — too new for the cutoff, retained.
	recent := ops.NewSession("claude", "recent", now)
	if err := s.UpsertSession(ctx, recent); err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteSkilllessSessions(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d sessions; want 1 (the old orphan only)", n)
	}
	if got, _ := s.GetSession(ctx, orphan.ID); got != nil {
		t.Errorf("old skill-less orphan survived sweep")
	}
	if got, _ := s.GetSession(ctx, kept.ID); got == nil {
		t.Errorf("skill-bearing session was wrongly swept")
	}
	if got, _ := s.GetSession(ctx, recent.ID); got == nil {
		t.Errorf("recent skill-less session swept before cutoff")
	}
}

func TestCountSessions(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seed := []string{"claude", "claude", "cursor"}
	for i, agent := range seed {
		// Distinct correlation key per session so the deterministic UUIDs
		// don't collide into a single upserted row.
		sess := ops.NewSession(agent, fmt.Sprintf("s-%s-%d", agent, i), time.Now().UTC())
		if err := s.UpsertSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.CountSessions(ctx, "claude"); n != 2 {
		t.Errorf("claude sessions=%d want 2", n)
	}
	if n, _ := s.CountSessions(ctx, ""); n != 3 {
		t.Errorf("all sessions=%d want 3", n)
	}
}
