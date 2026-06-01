package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/config"
	"github.com/spf13/cobra"
)

var (
	sessionsSince string
	sessionsAgent string
	sessionsLimit int
)

var auditSessionsCmd = &cobra.Command{
	Use:   "sessions [show <id>]",
	Short: "List recorded agent sessions",
	Long: `Lists agent sessions newest-first with per-session counts. Use
'qvr audit sessions show <id>' to see every event in one session.`,
	Args: cobra.ArbitraryArgs,
	RunE: runAuditSessions,
}

func init() {
	auditSessionsCmd.Flags().StringVar(&sessionsSince, "since", "", "only sessions started since this time (e.g. 7d, 24h, or RFC3339)")
	auditSessionsCmd.Flags().StringVar(&sessionsAgent, "agent", "", "filter by agent name (e.g. claude-code, opencode)")
	auditSessionsCmd.Flags().IntVar(&sessionsLimit, "limit", 50, "maximum sessions to show (0 = no limit)")
	auditCmd.AddCommand(auditSessionsCmd)
}

func runAuditSessions(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// `sessions show <id>` subform.
	if len(args) >= 1 && args[0] == "show" {
		if len(args) < 2 {
			return fmt.Errorf("usage: qvr audit sessions show <id>")
		}
		return showSession(cmd, cfg, args[1])
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown argument %q (did you mean 'sessions show <id>'?)", args[0])
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]any{})
		}
		printer.Info("no sessions recorded yet")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	var since *time.Time
	if sessionsSince != "" {
		t, perr := parseTimeFlag(sessionsSince)
		if perr != nil {
			return fmt.Errorf("invalid --since: %w", perr)
		}
		since = &t
	}

	sessions, err := s.ListSessions(cmd.Context(), since, nil, sessionsAgent, sessionsLimit)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	if outputFormat == "json" {
		// Never emit a bare `null` on an empty result — keep it pipe-safe
		// (mirror the missing-DB path above, which returns `[]`).
		if len(sessions) == 0 {
			return printer.JSON([]any{})
		}
		return printer.JSON(sessions)
	}
	if len(sessions) == 0 {
		printer.Info("no sessions recorded yet")
		return nil
	}
	headers := []string{"STARTED", "AGENT", "ACTIONS", "WRITES", "CMDS", "ERRORS", "SKILLS"}
	rows := make([][]string, 0, len(sessions))
	for _, sess := range sessions {
		rows = append(rows, []string{
			sess.StartedAt.Local().Format("01-02 15:04"),
			sess.AgentName,
			fmt.Sprintf("%d", sess.TotalActions),
			fmt.Sprintf("%d", sess.FilesWritten),
			fmt.Sprintf("%d", sess.CommandsExecuted),
			fmt.Sprintf("%d", sess.Errors),
			strings.Join(sess.SkillsTouched, ","),
		})
	}
	printer.Table(headers, rows)
	return nil
}

func showSession(cmd *cobra.Command, cfg *config.Config, idArg string) error {
	id, err := uuid.Parse(idArg)
	if err != nil {
		return fmt.Errorf("invalid session id %q: %w", idArg, err)
	}
	if !auditDBExists(cfg) {
		return renderEmptyEvents()
	}
	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	events, err := s.GetEventsBySession(cmd.Context(), id)
	if err != nil {
		return fmt.Errorf("get session events: %w", err)
	}
	if outputFormat == "json" {
		return printer.JSON(events)
	}
	if len(events) == 0 {
		printer.Info("no events for that session")
		return nil
	}
	headers := []string{"TIME", "SKILL", "ACTION", "TOOL", "STATUS", "TARGET"}
	rows := make([][]string, 0, len(events))
	for _, e := range events {
		rows = append(rows, []string{
			e.Timestamp.Local().Format("15:04:05"),
			e.SkillName,
			string(e.ActionType),
			e.ToolName,
			string(e.ResultStatus),
			eventTarget(e),
		})
	}
	printer.Table(headers, rows)
	return nil
}
