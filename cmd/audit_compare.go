package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

var (
	compareVersions    []string
	compareToolOnly    bool
	compareIncludePath bool // deprecated: path activations now count by default
	compareSince       string
	compareMetric      string
	compareByAgent     bool
)

var auditCompareCmd = &cobra.Command{
	Use:   "compare <skill>",
	Short: "Compare a skill's run cohorts by content version",
	Long: `Buckets a skill's recorded runs by the content version that produced them
and prints the cohorts side by side with their run-status breakdown — the
before/after evidence for the skill-evolution loop.

The version coordinate is the run-time content hash captured from the trace —
the verbatim body the run loaded, or the subtree hash a qvr-managed load proves
— and otherwise the proven commit sha. It is fixed by the recorded bytes, never
re-read from disk at discover time, so a run is bucketed by the version that
actually produced it even after the skill is switched, edited, or uninstalled.

With no --version, the two most-recent versions are compared (the usual
edit-then-rerun case). Pass --version twice to pin an explicit pair; each value
is a prefix of the VERSION shown by 'qvr audit logs' (a content-hash hex or a
short commit sha).

VERSION (the content hash) is the durable, run-immutable coordinate — it is what
distinguishes the cohorts. REF is a human label and is shown only for the cohort
whose content matches the version installed right now; older cohorts show '—'
because a symlink-recorded load carries no sha, so their version name cannot be
recovered after a switch (an honest blank, never a guessed name).

Every counted run is a genuine load — the skill's SKILL.md was opened. By default
both detection mechanisms count: a first-class skill tool call AND a SKILL.md load
read by path (how tool-less agents like codex invoke a skill). Pass --tool-only to
restrict to first-class tool calls. Incidental touches of a skill's other files
are never counted (they stay tool actions, not loads). Runs with no durable
version coordinate are reported as an explicit 'unknown' cohort, never dropped.

SCORE is your quality dimension: the mean of the BYO-grader verdicts attached to
each cohort's runs with 'qvr audit annotate' (a pass-rate), over the metric named
by --metric. It is '—' until you grade — qvr stores the number, it never computes
it. A cohort with no graded run stays blank, never a silent pass.

TOKENS is the cost dimension: in/out totals over the sessions the cohort's runs
fired in — session-attributed EXPOSURE, not exclusive cost (a session that fired
two skills lends its tokens to both), and n/a when the agent reported no usage.
SCORE and TOKENS together are the (quality, cost) frontier.

--by-agent splits each version into one row per agent — the {version × agent}
matrix, since the best version can differ by agent (one agent wants a terse skill,
another a discursive one). PARETO marks the cells on each agent's (quality↑,
cost↓) frontier with '*'.

This presents the cohorts and their deltas; it does NOT declare a winner. The
run status is whether a run errored / was interrupted / blocked — not a quality
grade; SCORE is whatever your grader said; PARETO is a reading aid, never a
verdict. Read the verbatim traces ('qvr audit export') to judge quality, or
attach a grade and read it here.`,
	Args: cobra.ExactArgs(1),
	RunE: runAuditCompare,
}

func init() {
	f := auditCompareCmd.Flags()
	f.StringArrayVar(&compareVersions, "version", nil, "content-hash prefix to compare (repeatable, max 2)")
	f.BoolVar(&compareToolOnly, "tool-only", false, "count only first-class skill tool calls, not SKILL.md loads read by path")
	f.BoolVar(&compareIncludePath, "include-path", false, "deprecated no-op: path SKILL.md loads now count by default")
	_ = f.MarkDeprecated("include-path", "path SKILL.md loads now count by default; use --tool-only to restrict")
	f.StringVar(&compareSince, "since", "", "only runs since this time (e.g. 30d, 24h, or RFC3339)")
	f.StringVar(&compareMetric, "metric", "score", "BYO-grader metric to show as the per-cohort SCORE (see `qvr audit annotate`)")
	f.BoolVar(&compareByAgent, "by-agent", false, "split each version into one row per agent (the {version × agent} matrix)")
	auditCmd.AddCommand(auditCompareCmd)
}

// compareResult is the JSON shape the evolution loop consumes.
type compareResult struct {
	Skill              string                      `json:"skill"`
	Activation         string                      `json:"activation"` // "tool" or "" (all)
	Cohorts            []*store.SkillContentCohort `json:"cohorts"`
	UnknownActivations int64                       `json:"unknownActivations"`
}

func runAuditCompare(cmd *cobra.Command, args []string) error {
	skill := args[0]
	if len(compareVersions) > 2 {
		return fmt.Errorf("compare at most two versions, got %d", len(compareVersions))
	}
	if len(compareVersions) == 2 && compareVersions[0] == compareVersions[1] {
		return fmt.Errorf("--version passed twice with the same prefix %q; provide two distinct versions to compare", compareVersions[0])
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	f := &store.MetricsFilter{Skill: skill, Metric: compareMetric, ByAgent: compareByAgent}
	if compareToolOnly {
		f.Activation = "tool"
	}
	if compareSince != "" {
		t, perr := parseTimeFlag(compareSince)
		if perr != nil {
			return fmt.Errorf("invalid --since: %w", perr)
		}
		f.Since = &t
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON(compareResult{Skill: skill, Cohorts: []*store.SkillContentCohort{}})
		}
		printer.Info("No runs recorded yet")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	all, err := s.SkillContentRollup(cmd.Context(), f)
	if err != nil {
		return fmt.Errorf("compare cohorts: %w", err)
	}
	// Each cohort's ref/commit is the version FROZEN on its spans at ingest
	// (enrichment proves identity only against the run-immutable body, so a
	// cohort is labelled with the version it actually ran — never the current
	// checkout). We therefore trust the stored label and never re-resolve live;
	// the render falls back to "unknown" when a cohort has no proven version.

	selected, unknown, err := selectCompareCohorts(all, compareVersions)
	if err != nil {
		return err
	}
	markPareto(selected)
	if err := renderCompare(skill, f.Activation, selected, unknown); err != nil {
		return err
	}
	warnMetricMismatch(cmd.Context(), s, skill, selected)
	return nil
}

// warnMetricMismatch surfaces the silent-blank trap behind an all-'—' SCORE
// column: grades were recorded under one metric (e.g. 'exact') while compare is
// reading another (the default 'score'), so every cohort honestly reads '—' even
// though the grades join fine. When no selected cohort scored under the requested
// metric AND the skill carries grades under other metrics, it names them so the
// fix is one flag away. Text mode only (JSON callers see the raw cohorts); best-
// effort — a lookup error stays silent rather than failing a rendered compare.
func warnMetricMismatch(ctx context.Context, s store.Store, skill string, cohorts []*store.SkillContentCohort) {
	if outputFormat == "json" {
		return
	}
	for _, c := range cohorts {
		if c.MeanScore != nil {
			return // a cohort scored under the requested metric — no mismatch
		}
	}
	metrics, err := s.SkillScoreMetrics(ctx, skill)
	if err != nil {
		return
	}
	var others []string
	for _, m := range metrics {
		if m == compareMetric {
			return // grades DO exist under the requested metric — blanks are just ungraded cohorts
		}
		others = append(others, m)
	}
	if len(others) == 0 {
		return // no grades at all — the honest no-grades case, not a mismatch
	}
	printer.Info(fmt.Sprintf(
		"%q has grades under metric %s, but --metric is %q (no grades) — pass --metric %s to see them",
		skill, quoteList(others), compareMetric, others[0]))
}

// quoteList renders metric names as a quoted, comma-joined list for the hint.
func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(q, ", ")
}

// selectCompareCohorts picks the cohorts to compare, selecting by VERSION (not by
// row) so --by-agent keeps every agent cell of a chosen version together. It
// returns the count of runs that carried no content version (the unknown cohort).
// With explicit version prefixes, each must match a known version; otherwise the
// two newest versions are chosen. Cohorts arrive recency-sorted, and that order
// (then agent) is preserved in the result.
func selectCompareCohorts(all []*store.SkillContentCohort, versions []string) (selected []*store.SkillContentCohort, unknown int64, err error) {
	coordinated := make([]*store.SkillContentCohort, 0, len(all))
	order := make([]string, 0) // distinct content hashes, recency order
	seen := map[string]bool{}  // versions already in order
	for _, c := range all {
		if c.ContentHash == "" {
			unknown += c.Activations // may be several cells under --by-agent
			continue
		}
		coordinated = append(coordinated, c)
		if !seen[c.ContentHash] {
			seen[c.ContentHash] = true
			order = append(order, c.ContentHash)
		}
	}

	pick := map[string]bool{}
	if len(versions) == 0 {
		for _, h := range order {
			if len(pick) == 2 {
				break
			}
			pick[h] = true
		}
	} else {
		for _, v := range versions {
			h := matchVersion(order, v)
			if h == "" {
				return nil, unknown, fmt.Errorf("no cohort matches version %q (try a prefix from `qvr audit logs --skill <name>`)", v)
			}
			pick[h] = true
		}
	}
	for _, c := range coordinated {
		if pick[c.ContentHash] {
			selected = append(selected, c)
		}
	}
	return selected, unknown, nil
}

// matchVersion finds the content hash whose hex begins with prefix.
func matchVersion(order []string, prefix string) string {
	for _, h := range order {
		if strings.HasPrefix(strings.TrimPrefix(h, "sha256:"), prefix) {
			return h
		}
	}
	return ""
}

// markPareto flags the cells on the (quality, cost) frontier within each agent
// group: a cell that no other cell of the same agent dominates on (score ≥ and
// total tokens ≤, strictly better on at least one axis). Only cells carrying BOTH
// a graded score and a token cost are eligible — a cell missing either axis is
// incomparable, so it is neither marked nor allowed to dominate. The mark is a
// non-authoritative reading aid: compare still declares no winner, the keep/revert
// call stays the human's.
func markPareto(cohorts []*store.SkillContentCohort) {
	groups := map[string][]*store.SkillContentCohort{}
	for _, c := range cohorts {
		if c.MeanScore == nil || totalTokens(c) == nil {
			continue
		}
		groups[c.Agent] = append(groups[c.Agent], c)
	}
	for _, g := range groups {
		for _, c := range g {
			c.Pareto = !dominatedIn(g, c)
		}
	}
}

// dominatedIn reports whether any other cell in the agent group beats c on the
// frontier. Callers guarantee every cell carries both axes.
func dominatedIn(group []*store.SkillContentCohort, c *store.SkillContentCohort) bool {
	cs, ct := *c.MeanScore, *totalTokens(c)
	for _, o := range group {
		if o == c {
			continue
		}
		os, ot := *o.MeanScore, *totalTokens(o)
		if os >= cs && ot <= ct && (os > cs || ot < ct) {
			return true
		}
	}
	return false
}

// totalTokens is a cohort's scalar cost: input+output tokens, or nil when neither
// side reported usage (incomparable on cost). A nil side counts as 0 only when the
// other side is present, so a cohort with any usage data stays comparable.
func totalTokens(c *store.SkillContentCohort) *int64 {
	if c.InputTokens == nil && c.OutputTokens == nil {
		return nil
	}
	var t int64
	if c.InputTokens != nil {
		t += *c.InputTokens
	}
	if c.OutputTokens != nil {
		t += *c.OutputTokens
	}
	return &t
}

func renderCompare(skill, activation string, cohorts []*store.SkillContentCohort, unknown int64) error {
	if outputFormat == "json" {
		return printer.JSON(compareResult{
			Skill:              skill,
			Activation:         activation,
			Cohorts:            cohorts,
			UnknownActivations: unknown,
		})
	}
	if len(cohorts) == 0 {
		printer.Info(fmt.Sprintf("No content-versioned runs for %q yet", skill))
		return nil
	}
	headers := []string{"VERSION"}
	if compareByAgent {
		headers = append(headers, "AGENT")
	}
	headers = append(headers, "REF", "SCORE", "TOKENS", "PARETO",
		"RUNS", "SESSIONS", "SUCCESS", "FAILURE", "BLOCKED", "UNKNOWN", "FIRST", "LAST")
	rows := make([][]string, 0, len(cohorts))
	for _, c := range cohorts {
		row := []string{shortContentHash(c.ContentHash)}
		if compareByAgent {
			row = append(row, orDash(c.Agent))
		}
		row = append(row,
			derive.SkillVersionLabel(strings.TrimPrefix(c.Ref, skill+"/"), c.Commit),
			scoreCell(c),
			tokenPairCell(c.InputTokens, c.OutputTokens),
			paretoCell(c),
			fmt.Sprintf("%d", c.Activations),
			fmt.Sprintf("%d", c.Sessions),
			fmt.Sprintf("%d", c.Success),
			fmt.Sprintf("%d", c.Failure),
			fmt.Sprintf("%d", c.Blocked),
			fmt.Sprintf("%d", c.UnknownStatus),
			msTime(c.FirstFiredMs),
			msTime(c.LastFiredMs),
		)
		rows = append(rows, row)
	}
	printer.Table(headers, rows)
	if unknown > 0 {
		printer.Info(fmt.Sprintf("%d run(s) carried no content version (uncoordinated — excluded from the cohorts above)", unknown))
	}
	printer.Info("SCORE = graded pass-rate (`qvr audit annotate`); TOKENS = in/out over the cohort's sessions (exposure, not exclusive cost); PARETO * = on the (quality↑, cost↓) frontier — a hint, never a winner. Run status is NOT a quality grade.")
	return nil
}

// paretoCell marks a cell on the per-agent (quality, cost) frontier with '*'; a
// dominated or incomparable cell is blank. See markPareto — it is a reading aid,
// not a verdict.
func paretoCell(c *store.SkillContentCohort) string {
	if c.Pareto {
		return "*"
	}
	return ""
}

// scoreCell renders a cohort's BYO-grader score: the mean over its graded runs
// with that denominator (e.g. "1.00 (4)"), or "—" when no run is graded — an
// ungraded cohort is honestly blank, never a silent pass.
func scoreCell(c *store.SkillContentCohort) string {
	if c.MeanScore == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f (%d)", *c.MeanScore, c.Graded)
}
