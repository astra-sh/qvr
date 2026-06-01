package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/ops"
	"github.com/raks097/quiver/internal/ops/store"
	"github.com/spf13/cobra"
)

var (
	logsSkill     string
	logsAgent     string
	logsAction    string
	logsSession   string
	logsSince     string
	logsUntil     string
	logsLimit     int
	logsSensitive bool
)

var auditLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show recorded agent events",
	Long: `Query the audit trail. Filter by skill, agent, action type, session,
or time window. Events are shown newest-first.`,
	Args: cobra.NoArgs,
	RunE: runAuditLogs,
}

func init() {
	f := auditLogsCmd.Flags()
	f.StringVar(&logsSkill, "skill", "", "filter by skill name")
	f.StringVar(&logsAgent, "agent", "", "filter by agent name")
	f.StringVar(&logsAction, "action", "", "filter by action type (e.g. file_write, command_exec)")
	f.StringVar(&logsSession, "session", "", "filter by session id")
	f.StringVar(&logsSince, "since", "", "only events since this time (e.g. 1d, 12h, 30m, or RFC3339)")
	f.StringVar(&logsUntil, "until", "", "only events until this time (e.g. 1h or RFC3339)")
	f.IntVar(&logsLimit, "limit", 50, "maximum events to show (0 = no limit)")
	f.BoolVar(&logsSensitive, "sensitive", false, "show only events flagged sensitive")
	auditCmd.AddCommand(auditLogsCmd)
}

func runAuditLogs(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	filter, err := buildEventFilter()
	if err != nil {
		return err
	}

	// A missing DB means nothing has been recorded yet (ops enabled but no
	// events, or never enabled). Treat as an empty result rather than an
	// error — opening read-only would fail with "no such table".
	if !auditDBExists(cfg) {
		return renderEmptyEvents()
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	events, err := s.QueryEvents(cmd.Context(), filter)
	if err != nil {
		return fmt.Errorf("query events: %w", err)
	}

	if outputFormat == "json" {
		// Never emit a bare `null` — a nil slice would break downstream
		// jq/script pipelines. An empty result must serialise to `[]`.
		if events == nil {
			events = []*ops.Event{}
		}
		return printer.JSON(events)
	}
	if len(events) == 0 {
		printer.Info("no events match")
		return nil
	}
	headers := []string{"TIME", "AGENT", "SKILL", "ACTION", "TOOL", "STATUS", "TARGET"}
	rows := make([][]string, 0, len(events))
	for _, e := range events {
		rows = append(rows, []string{
			e.Timestamp.Local().Format("01-02 15:04:05"),
			e.AgentName,
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

// buildEventFilter translates the logs flags into a store.EventFilter.
func buildEventFilter() (*store.EventFilter, error) {
	f := &store.EventFilter{Limit: logsLimit}

	if logsSkill != "" {
		f.Skills = []string{logsSkill}
	}
	if logsAgent != "" {
		f.Agents = []string{logsAgent}
	}
	if logsAction != "" {
		f.Actions = []ops.ActionType{ops.ActionType(logsAction)}
	}
	if logsSensitive {
		t := true
		f.IsSensitive = &t
	}
	if logsSession != "" {
		id, err := uuid.Parse(logsSession)
		if err != nil {
			return nil, fmt.Errorf("invalid --session id %q: %w", logsSession, err)
		}
		f.SessionID = &id
	}
	if logsSince != "" {
		t, err := parseTimeFlag(logsSince)
		if err != nil {
			return nil, fmt.Errorf("invalid --since: %w", err)
		}
		f.Since = &t
	}
	if logsUntil != "" {
		t, err := parseTimeFlag(logsUntil)
		if err != nil {
			return nil, fmt.Errorf("invalid --until: %w", err)
		}
		f.Until = &t
	}
	return f, nil
}

// parseTimeFlag accepts either an RFC3339 timestamp or a relative duration
// like "1d", "12h", "30m" (interpreted as "ago"). The "d" (days) suffix is
// supported on top of Go's standard duration units.
func parseTimeFlag(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	dur, err := parseRelative(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().UTC().Add(-dur), nil
}

// parseRelative parses a duration, additionally supporting a trailing "d"
// for whole days (e.g. "7d").
func parseRelative(s string) (time.Duration, error) {
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(rest)
		if err != nil {
			return 0, fmt.Errorf("bad day count %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// eventTarget extracts a human-friendly "what" from an event's payload —
// the file path for file actions, the command for execs, else empty.
func eventTarget(e *ops.Event) string {
	switch e.ActionType {
	case ops.ActionFileRead:
		var p ops.FileReadPayload
		if e.DecodePayload(&p) == nil {
			if p.Path != "" {
				return p.Path
			}
			return p.Pattern
		}
	case ops.ActionFileWrite:
		var p ops.FileWritePayload
		if e.DecodePayload(&p) == nil {
			return p.Path
		}
	case ops.ActionCommandExec:
		var p ops.CommandExecPayload
		if e.DecodePayload(&p) == nil {
			return truncTarget(p.Command)
		}
	}
	return ""
}

func truncTarget(s string) string {
	const max = 50
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
