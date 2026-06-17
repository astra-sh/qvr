package cmd

import (
	"fmt"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

var (
	promoteReason   string
	promoteForce    bool
	promoteSkillDir string
)

var opsPromoteCmd = &cobra.Command{
	Use:   "promote <skill>",
	Short: "Gate a skill's advancement on a passing eval",
	Long: `Refuses to advance a skill whose currently-locked commit has no passing eval
run, unless --force-no-eval is given. This is the evidence gate the
self-improvement loop ends on: a drafted improvement only ships once
'qvr ops eval run' has recorded a pass for that exact commit.

It is a check, not a state change: it reports whether the locked commit is
backed by evidence, so a loop or CI step can branch on the exit code.`,
	Args: cobra.ExactArgs(1),
	RunE: runOpsPromote,
}

func init() {
	opsPromoteCmd.Flags().StringVar(&promoteReason, "reason", "", "why the skill is being promoted (recorded in the message)")
	opsPromoteCmd.Flags().BoolVar(&promoteForce, "force-no-eval", false, "promote even without a passing eval (records the override)")
	opsPromoteCmd.Flags().StringVar(&promoteSkillDir, "skill-dir", "", "skill directory (default: the installed project skill)")
	opsCmd.AddCommand(opsPromoteCmd)
}

func runOpsPromote(cmd *cobra.Command, args []string) error {
	rs, err := resolveSkill(args[0], promoteSkillDir)
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
	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	passing, err := latestPassingEval(cmd, s, rs)
	if err != nil {
		return fmt.Errorf("look up eval history for %s: %w", rs.Name, err)
	}
	decision := promoteDecision(rs, passing, promoteForce, promoteReason)

	if outputFormat == "json" {
		if err := printer.JSON(decision); err != nil {
			return err
		}
	} else {
		printer.Info(decision.Message)
	}
	if !decision.Promoted {
		return fmt.Errorf("refusing to promote %s: no passing eval for commit %s (run `qvr ops eval run %s`, or pass --force-no-eval)",
			rs.Name, shortCommit(rs.Commit), rs.Name)
	}
	return nil
}

// promoteDecisionResult is the JSON/text shape of a promote check.
type promoteDecisionResult struct {
	Skill    string `json:"skill"`
	Commit   string `json:"commit,omitempty"`
	Promoted bool   `json:"promoted"`
	Forced   bool   `json:"forced,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message"`
}

func promoteDecision(rs *resolvedSkill, passing *store.EvalRunRow, force bool, reason string) promoteDecisionResult {
	d := promoteDecisionResult{Skill: rs.Name, Commit: shortCommit(rs.Commit), Reason: reason}
	switch {
	case passing != nil:
		d.Promoted = true
		d.Message = fmt.Sprintf("%s @ %s is backed by a passing eval (run #%d) — clear to promote", rs.Name, shortCommit(rs.Commit), passing.ID)
	case force:
		d.Promoted = true
		d.Forced = true
		d.Message = fmt.Sprintf("%s @ %s promoted WITHOUT a passing eval (--force-no-eval)", rs.Name, shortCommit(rs.Commit))
	default:
		d.Message = fmt.Sprintf("%s @ %s has no passing eval", rs.Name, shortCommit(rs.Commit))
	}
	return d
}

// latestPassingEval returns the newest passing eval run for the skill's locked
// commit, or nil when there is none. A skill with no locked commit ("") can't be
// evidence-gated by commit, so it returns nil (promotion then requires
// --force-no-eval). A store error is propagated, NOT swallowed — otherwise a
// transient DB failure would read as "no passing eval" and silently block (or
// mislead) a CI gate.
func latestPassingEval(cmd *cobra.Command, s store.Store, rs *resolvedSkill) (*store.EvalRunRow, error) {
	if rs.Commit == "" {
		return nil, nil
	}
	runs, err := s.ListEvalRuns(cmd.Context(), &store.EvalRunFilter{SkillName: rs.Name, SkillCommit: rs.Commit})
	if err != nil {
		return nil, err
	}
	for _, r := range runs { // newest-first
		if r.Pass {
			return r, nil
		}
	}
	return nil, nil
}
