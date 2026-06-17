package cmd

import (
	"fmt"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/eval"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	evalSkillDir string
	evalSuite    string
	evalSession  string
)

var opsEvalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Grade a skill's captured sessions against its evals.yaml",
	Long: `Grades a skill against its evals.yaml manifest, deterministically, over the
sessions qvr already captured — no agent run, no model call. The verdict is
recorded keyed by the skill's locked commit, so 'qvr ops lineage' can show the
score move across versions.`,
	RunE: rejectUnknownSubcommand,
}

var opsEvalRunCmd = &cobra.Command{
	Use:   "run <skill>",
	Short: "Run a skill's evals against its latest captured session",
	Long: `Grades the skill's most recent captured session (or --session <id>) against
the suite(s) in its evals.yaml, records the result keyed by the locked commit,
and exits non-zero if any case fails — so a CI step can gate a change.

By default the installed project skill dir is used; pass --skill-dir to evaluate
a specific directory (e.g. the writable copy an evolution loop is editing).`,
	Args: cobra.ExactArgs(1),
	RunE: runOpsEval,
}

func init() {
	opsEvalRunCmd.Flags().StringVar(&evalSkillDir, "skill-dir", "", "skill directory holding evals.yaml (default: the installed project skill)")
	opsEvalRunCmd.Flags().StringVar(&evalSuite, "suite", "", "evaluate only this suite (default: all)")
	opsEvalRunCmd.Flags().StringVar(&evalSession, "session", "", "grade this session id (default: the skill's most recent)")
	opsEvalCmd.AddCommand(opsEvalRunCmd)
	opsCmd.AddCommand(opsEvalCmd)
}

func runOpsEval(cmd *cobra.Command, args []string) error {
	rs, err := resolveSkill(args[0], evalSkillDir)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !auditDBExists(cfg) {
		return fmt.Errorf("no audit database yet — run `qvr audit discover` first")
	}
	// Eval writes an eval_runs row, so open read-write (applies migration 0009).
	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	in := eval.RunInput{SkillName: rs.Name, SkillDir: rs.Dir, Suite: evalSuite}
	if evalSession != "" {
		id, perr := uuid.Parse(evalSession)
		if perr != nil {
			return fmt.Errorf("invalid --session id %q: %w", evalSession, perr)
		}
		in.SessionID = &id
	}

	result, err := eval.Run(cmd.Context(), s, in)
	if err != nil {
		if names := loadEvalSuiteNames(rs.Dir); evalSuite != "" && len(names) > 0 {
			return fmt.Errorf("%w (available suites: %s)", err, strings.Join(names, ", "))
		}
		return err
	}

	if _, err := s.PutEvalRun(cmd.Context(), evalRunRow(rs, result, evalSuite)); err != nil {
		return fmt.Errorf("record eval run: %w", err)
	}

	if rerr := renderEvalResult(result); rerr != nil {
		return rerr
	}
	// A failed gate exits non-zero so CI / the evolution loop can branch on it.
	if !result.Pass {
		return fmt.Errorf("eval failed: %d of %d cases failed for %s", result.Failed, result.Passed+result.Failed, rs.Name)
	}
	return nil
}

// evalRunRow flattens a run result into the persisted row + its case rows. The
// requested suite is passed in (not read from the flag global) so the function
// is pure and testable in isolation.
func evalRunRow(rs *resolvedSkill, r *eval.RunResult, suite string) *store.EvalRunRow {
	if suite == "" {
		suite = "*"
	}
	row := &store.EvalRunRow{
		SkillName:   rs.Name,
		SkillCommit: rs.Commit,
		Suite:       suite,
		SessionID:   r.SessionID,
		Passed:      r.Passed,
		Failed:      r.Failed,
		Pass:        r.Pass,
	}
	for _, su := range r.Suites {
		for _, c := range su.Cases {
			row.Cases = append(row.Cases, store.EvalCaseRow{
				Suite: su.Suite, Case: c.Case, Pass: c.Pass, Detail: caseDetail(c),
			})
		}
	}
	return row
}

// caseDetail joins the failing graders' details into a one-line reason.
func caseDetail(c eval.CaseResult) string {
	if c.Pass {
		return ""
	}
	var parts []string
	for _, g := range c.Graders {
		if !g.Pass {
			parts = append(parts, fmt.Sprintf("%s: %s", g.Type, g.Detail))
		}
	}
	return strings.Join(parts, "; ")
}

func renderEvalResult(r *eval.RunResult) error {
	if outputFormat == "json" {
		return printer.JSON(r)
	}
	verdict := "PASS"
	if !r.Pass {
		verdict = "FAIL"
	}
	printer.Info(fmt.Sprintf("%s — %s: %d passed, %d failed (session %s)",
		verdict, r.Skill, r.Passed, r.Failed, shortID(r.SessionID)))
	headers := []string{"SUITE", "CASE", "RESULT", "DETAIL"}
	rows := make([][]string, 0)
	for _, su := range r.Suites {
		for _, c := range su.Cases {
			res := "pass"
			if !c.Pass {
				res = "FAIL"
			}
			rows = append(rows, []string{su.Suite, c.Case, res, clipCell(caseDetail(c), 60)})
		}
	}
	if len(rows) > 0 {
		printer.Table(headers, rows)
	}
	return nil
}

// shortID clips a session id to its first segment for compact display.
func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}
