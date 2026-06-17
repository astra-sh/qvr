package cmd

import (
	"fmt"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	annotationsSession string
	annotationsSkill   string
	annotationsSince   string
)

var auditAnnotationsCmd = &cobra.Command{
	Use:   "annotations",
	Short: "List human verdicts recorded on captured sessions",
	Long: `Lists the human verdicts recorded with 'qvr audit annotate', newest-first.
These are the outer-loop feedback signal a self-improvement loop reads to decide
what to fix. Scope with --session and/or --skill.`,
	Args: cobra.NoArgs,
	RunE: runAuditAnnotations,
}

func init() {
	auditAnnotationsCmd.Flags().StringVar(&annotationsSession, "session", "", "filter to one session id")
	auditAnnotationsCmd.Flags().StringVar(&annotationsSkill, "skill", "", "filter to one skill")
	auditAnnotationsCmd.Flags().StringVar(&annotationsSince, "since", "", "only verdicts since this time (e.g. 7d, 24h, or RFC3339)")
	auditCmd.AddCommand(auditAnnotationsCmd)
}

func runAuditAnnotations(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]any{})
		}
		printer.Info("No annotations recorded yet")
		return nil
	}

	f := &store.AnnotationFilter{Skill: annotationsSkill}
	if annotationsSession != "" {
		id, perr := uuid.Parse(annotationsSession)
		if perr != nil {
			return fmt.Errorf("invalid --session id %q: %w", annotationsSession, perr)
		}
		f.SessionID = &id
	}
	if annotationsSince != "" {
		t, perr := parseTimeFlag(annotationsSince)
		if perr != nil {
			return fmt.Errorf("invalid --since: %w", perr)
		}
		f.Since = &t
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	rows, err := s.ListAnnotations(cmd.Context(), f)
	if err != nil {
		return fmt.Errorf("list annotations: %w", err)
	}
	return renderAnnotations(rows)
}

func renderAnnotations(rows []*store.AnnotationRow) error {
	if outputFormat == "json" {
		if len(rows) == 0 {
			return printer.JSON([]any{})
		}
		return printer.JSON(rows)
	}
	if len(rows) == 0 {
		printer.Info("No annotations recorded yet")
		return nil
	}
	headers := []string{"CREATED", "OUTCOME", "SKILL", "AUTHOR", "NOTE", "SESSION ID"}
	out := make([][]string, 0, len(rows))
	for _, a := range rows {
		skill := a.Skill
		if skill == "" {
			skill = "(session)"
		}
		out = append(out, []string{
			a.CreatedAt.Local().Format("01-02 15:04"),
			a.Outcome,
			skill,
			a.Author,
			clipCell(a.Note, 48),
			a.SessionID.String(),
		})
	}
	printer.Table(headers, out)
	return nil
}
