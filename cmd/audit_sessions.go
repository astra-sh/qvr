package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
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
	Long: `Lists recorded agent sessions newest-first from the unified session model
(title, model, turn/tool counts, skills used). Use 'qvr audit sessions show
<id>' to print one session's verbatim raw lines (equivalent to
'qvr audit raw --session <id>').`,
	Args: cobra.ArbitraryArgs,
	RunE: runAuditSessions,
}

func init() {
	auditSessionsCmd.Flags().StringVar(&sessionsSince, "since", "", "only sessions started since this time (e.g. 7d, 24h, or RFC3339)")
	auditSessionsCmd.Flags().StringVar(&sessionsAgent, "agent", "", "filter by agent name (e.g. claude, codex)")
	auditSessionsCmd.Flags().IntVar(&sessionsLimit, "limit", 50, "maximum sessions to show (0 = no limit)")
	auditCmd.AddCommand(auditSessionsCmd)
}

func runAuditSessions(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if len(args) >= 1 && args[0] == "show" {
		if len(args) < 2 {
			return fmt.Errorf("usage: qvr audit sessions show <id>")
		}
		return showSession(cmd, cfg, args[1])
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown argument %q — did you mean `qvr audit sessions show <id>`?", args[0])
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]any{})
		}
		printer.Info("No sessions recorded yet")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	f := &store.SessionMetaFilter{Agent: canonicalAgentFlag(sessionsAgent), Limit: sessionsLimit}
	if sessionsSince != "" {
		t, perr := parseTimeFlag(sessionsSince)
		if perr != nil {
			return fmt.Errorf("invalid --since: %w", perr)
		}
		f.Since = &t
	}

	sessions, err := s.ListSessionMeta(cmd.Context(), f)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	backfillSessionSkills(cmd.Context(), s, sessions)

	if outputFormat == "json" {
		return renderSessionsJSON(cmd.Context(), s, sessions)
	}
	return renderSessions(sessions)
}

// sessionView is the JSON shape: the session header plus the per-skill version
// coordinates (#2), so a consumer reads which version each skill ran without a
// time-window cross-reference into the spans table.
type sessionView struct {
	*store.SessionMetaRow
	SkillVersions []skillVersionView   `json:"skill_versions,omitempty"`
	Scores        []store.SessionScore `json:"scores,omitempty"`
}

type skillVersionView struct {
	Skill       string `json:"skill"`
	Version     string `json:"version"` // ref → short commit → "unknown"
	Commit      string `json:"commit,omitempty"`
	SubtreeHash string `json:"subtree_hash,omitempty"`
}

// buildSessionViews enriches sessions with their per-skill version coordinates,
// read from the SKILL spans via Store.SkillVersionsForSessions (the single
// source of truth) and labeled through the shared derive.SkillVersionLabel so
// the JSON agrees across the CLI, the UI, and compare (degrading to "unknown"
// the same way). Shared by `qvr audit sessions` and the UI's /api/sessions so
// both return the identical skill_versions shape from one path.
func buildSessionViews(ctx context.Context, s store.Store, sessions []*store.SessionMetaRow) ([]sessionView, error) {
	ids := make([]string, len(sessions))
	for i, sess := range sessions {
		ids[i] = sess.SessionID.String()
	}
	coords, err := s.SkillVersionsForSessions(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("session skill versions: %w", err)
	}
	// BYO-grader verdicts joined back from session_score on the same path the
	// version coordinates take — one read, so every consumer (CLI JSON + UI list)
	// gets the identical scores shape next to skill_versions.
	scores, err := s.ScoresForSessions(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("session scores: %w", err)
	}
	views := make([]sessionView, len(sessions))
	for i, sess := range sessions {
		v := sessionView{SessionMetaRow: sess}
		for _, c := range coords[sess.SessionID.String()] {
			v.SkillVersions = append(v.SkillVersions, skillVersionView{
				Skill:       c.Skill,
				Version:     derive.SkillVersionLabel(c.Version, c.Commit),
				Commit:      c.Commit,
				SubtreeHash: c.SubtreeHash,
			})
		}
		v.Scores = scores[sess.SessionID.String()]
		views[i] = v
	}
	return views, nil
}

// renderSessionsJSON emits sessions enriched with per-skill version coordinates.
func renderSessionsJSON(ctx context.Context, s store.Store, sessions []*store.SessionMetaRow) error {
	if len(sessions) == 0 {
		return printer.JSON([]any{})
	}
	views, err := buildSessionViews(ctx, s, sessions)
	if err != nil {
		return err
	}
	return printer.JSON(views)
}

// backfillSessionSkills repopulates the SKILLS rollup for sessions whose stored
// session_meta.skills came back empty (a derive-time miss), recomputing it from
// the spans table on read. Best-effort: a query error leaves the column blank
// rather than failing the listing.
func backfillSessionSkills(ctx context.Context, s store.Store, sessions []*store.SessionMetaRow) {
	var need []string
	for _, sess := range sessions {
		if len(sess.Skills) == 0 {
			need = append(need, sess.SessionID.String())
		}
	}
	if len(need) == 0 {
		return
	}
	byID, err := s.SkillsForSessions(ctx, need)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if len(sess.Skills) == 0 {
			if sk := byID[sess.SessionID.String()]; len(sk) > 0 {
				sess.Skills = sk
			}
		}
	}
}

// renderSessions emits the session list as JSON or a newest-first table.
func renderSessions(sessions []*store.SessionMetaRow) error {
	if outputFormat == "json" {
		if len(sessions) == 0 {
			return printer.JSON([]any{})
		}
		return printer.JSON(sessions)
	}
	if len(sessions) == 0 {
		printer.Info("No sessions recorded yet")
		return nil
	}
	headers := []string{"STARTED", "AGENT", "TITLE", "TURNS", "TOOLS", "DURATION", "TOKENS", "SKILLS", "SESSION ID"}
	rows := make([][]string, 0, len(sessions))
	for _, sess := range sessions {
		rows = append(rows, []string{
			time.UnixMilli(sess.StartedMs).Local().Format("01-02 15:04"),
			sess.AgentName,
			clipCell(sess.Title, 48),
			fmt.Sprintf("%d", sess.Turns),
			fmt.Sprintf("%d", sess.Tools),
			durationCell(sess.DurationMs()),
			tokenPairCell(sess.TokensIn, sess.TokensOut),
			strings.Join(sess.Skills, ","),
			sess.SessionID.String(),
		})
	}
	printer.Table(headers, rows)
	return nil
}

// tokenPairCell renders session token totals as "in/out", honest about
// absence: a session whose store reported no usage reads n/a, never 0.
func tokenPairCell(in, out *int64) string {
	if in == nil && out == nil {
		return "n/a"
	}
	return abbrevCount(in) + "/" + abbrevCount(out)
}

// durationCell renders a session wall-clock duration (ms) compactly: "—" when
// unknown (0), else fractional seconds, m:ss, or h:mm.
func durationCell(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	s := ms / 1000
	switch {
	case s < 60:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	}
}

// abbrevCount renders a nullable count compactly (8.4k, 1.2M); nil → "-".
func abbrevCount(v *int64) string {
	if v == nil {
		return "-"
	}
	n := *v
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// clipCell truncates a table cell to n runes with an ellipsis.
func clipCell(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// canonicalAgentFlag normalizes a user-supplied agent name/alias to its
// canonical target name, so filters match the stored unified model.
func canonicalAgentFlag(name string) string {
	if name == "" {
		return ""
	}
	if c, ok := model.CanonicalTarget(name); ok {
		return c
	}
	return name
}

// showSession prints one session's verbatim raw lines.
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

	rows, err := s.QueryRawTraces(cmd.Context(), &store.RawTraceFilter{SessionID: &id})
	if err != nil {
		return fmt.Errorf("get session traces: %w", err)
	}
	if outputFormat == "json" {
		return printer.JSON(rows)
	}
	if len(rows) == 0 {
		printer.Info("No traces for that session")
		return nil
	}
	w := cmd.OutOrStdout()
	for _, r := range rows {
		fmt.Fprintln(w, string(r.Raw))
	}
	return nil
}
