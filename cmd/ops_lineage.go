package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

var lineageSince string

var opsLineageCmd = &cobra.Command{
	Use:   "lineage <skill>",
	Short: "Show a skill's eval verdicts and human annotations over time",
	Long: `Merges a skill's eval runs and human annotations into one time-ordered
timeline, each eval keyed to the locked commit that was graded. This is how the
self-improvement loop reads "at this commit the suite failed; after the fix it
passed" — the lock × evidence join made legible.`,
	Args: cobra.ExactArgs(1),
	RunE: runOpsLineage,
}

func init() {
	opsLineageCmd.Flags().StringVar(&lineageSince, "since", "", "only entries since this time (e.g. 30d, 24h, or RFC3339)")
	opsCmd.AddCommand(opsLineageCmd)
}

// lineageEntry is one row of the merged timeline.
type lineageEntry struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // "eval" or "annotation"
	Commit  string    `json:"commit,omitempty"`
	Detail  string    `json:"detail"`
	Pass    *bool     `json:"pass,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
}

func runOpsLineage(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]any{})
		}
		printer.Info("Nothing recorded yet")
		return nil
	}
	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	var since *time.Time
	if lineageSince != "" {
		t, perr := parseTimeFlag(lineageSince)
		if perr != nil {
			return fmt.Errorf("invalid --since: %w", perr)
		}
		since = &t
	}

	runs, err := s.ListEvalRuns(cmd.Context(), &store.EvalRunFilter{SkillName: name, Since: since})
	if err != nil {
		return fmt.Errorf("list eval runs: %w", err)
	}
	anns, err := s.ListAnnotations(cmd.Context(), &store.AnnotationFilter{Skill: name, Since: since})
	if err != nil {
		return fmt.Errorf("list annotations: %w", err)
	}

	timeline := mergeLineage(runs, anns)
	return renderLineage(timeline)
}

// mergeLineage interleaves eval runs and annotations into one newest-first
// timeline.
func mergeLineage(runs []*store.EvalRunRow, anns []*store.AnnotationRow) []lineageEntry {
	var out []lineageEntry
	for _, r := range runs {
		pass := r.Pass
		out = append(out, lineageEntry{
			At: r.StartedAt, Kind: "eval", Commit: shortCommit(r.SkillCommit), Pass: &pass,
			Detail: fmt.Sprintf("suite %s: %d passed, %d failed", r.Suite, r.Passed, r.Failed),
		})
	}
	for _, a := range anns {
		out = append(out, lineageEntry{
			At: a.CreatedAt, Kind: "annotation", Outcome: a.Outcome,
			Detail: annotationDetail(a),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func annotationDetail(a *store.AnnotationRow) string {
	if a.Note != "" {
		return fmt.Sprintf("%s — %s", a.Outcome, a.Note)
	}
	return a.Outcome
}

func renderLineage(timeline []lineageEntry) error {
	if outputFormat == "json" {
		if len(timeline) == 0 {
			return printer.JSON([]any{})
		}
		return printer.JSON(timeline)
	}
	if len(timeline) == 0 {
		printer.Info("No eval runs or annotations for that skill yet")
		return nil
	}
	headers := []string{"WHEN", "KIND", "COMMIT", "RESULT", "DETAIL"}
	rows := make([][]string, 0, len(timeline))
	for _, e := range timeline {
		result := ""
		switch {
		case e.Pass != nil && *e.Pass:
			result = "pass"
		case e.Pass != nil:
			result = "FAIL"
		case e.Outcome != "":
			result = e.Outcome
		}
		rows = append(rows, []string{
			e.At.Local().Format("01-02 15:04"),
			e.Kind, e.Commit, result, clipCell(e.Detail, 56),
		})
	}
	printer.Table(headers, rows)
	return nil
}

// shortCommit clips a git SHA to 7 chars, leaving "" (no lock) untouched.
func shortCommit(c string) string {
	if len(c) >= 7 {
		return c[:7]
	}
	return c
}
