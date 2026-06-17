package cmd

import (
	"fmt"
	"os/user"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	annotateOutcome string
	annotateNote    string
	annotateSkill   string
	annotateAuthor  string
)

var auditAnnotateCmd = &cobra.Command{
	Use:   "annotate <session-id>",
	Short: "Record a human verdict on a captured session",
	Long: `Attaches a durable human verdict — the outer-loop feedback signal — to a
captured session: did the skill do the right thing, and why?

Unlike the auto-derived span outcome (which only reflects whether a tool
errored), an annotation is a reviewer's judgement ("this triage was wrong, the
issue was ambiguous") and is never overwritten by 'qvr audit rederive' — it
lives outside the regenerable spans/session_meta projection. A self-improvement
loop reads these verdicts (alongside the auto outcome) to decide what to fix.

Scope a verdict to one skill with --skill, or omit it for a whole-session
verdict. Annotations are append-only: a session can accrue several over time.`,
	Args: cobra.ExactArgs(1),
	RunE: runAuditAnnotate,
}

func init() {
	auditAnnotateCmd.Flags().StringVar(&annotateOutcome, "outcome", "", "the verdict (e.g. good, bad, blocked) — required")
	auditAnnotateCmd.Flags().StringVar(&annotateNote, "note", "", "why — the reason for the verdict")
	auditAnnotateCmd.Flags().StringVar(&annotateSkill, "skill", "", "scope the verdict to one skill (omit for whole-session)")
	auditAnnotateCmd.Flags().StringVar(&annotateAuthor, "author", "", "verdict author (defaults to the current OS user)")
	auditCmd.AddCommand(auditAnnotateCmd)
}

func runAuditAnnotate(cmd *cobra.Command, args []string) error {
	if annotateOutcome == "" {
		return fmt.Errorf("--outcome is required (e.g. --outcome bad)")
	}
	id, err := uuid.Parse(args[0])
	if err != nil {
		return fmt.Errorf("invalid session id %q: %w", args[0], err)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !auditDBExists(cfg) {
		return fmt.Errorf("no audit database yet — run `qvr audit discover` first")
	}

	// A verdict is a write, so open read-write (this also applies migration
	// 0008 if the DB predates the annotations table).
	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	meta, err := s.GetSessionMeta(cmd.Context(), id)
	if err != nil {
		return fmt.Errorf("look up session: %w", err)
	}
	if meta == nil {
		return fmt.Errorf("no captured session %s — run `qvr audit sessions` to list ids", id)
	}

	a := &store.AnnotationRow{
		SessionID: id,
		Skill:     annotateSkill,
		Outcome:   annotateOutcome,
		Note:      annotateNote,
		Author:    annotateAuthor,
		CreatedAt: time.Now().UTC(),
	}
	if a.Author == "" {
		a.Author = currentUserName()
	}
	if err := s.PutAnnotation(cmd.Context(), a); err != nil {
		return err
	}

	if outputFormat == "json" {
		return printer.JSON(a)
	}
	scope := "session"
	if a.Skill != "" {
		scope = "skill " + a.Skill
	}
	printer.Info(fmt.Sprintf("Recorded %q verdict on %s (%s)", a.Outcome, scope, id))
	return nil
}

// currentUserName returns the OS username for verdict provenance, or "unknown".
func currentUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}
