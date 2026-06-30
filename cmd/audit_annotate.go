package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

var (
	annotateScore  float64
	annotateAgent  string
	annotateSkill  string
	annotateMetric string
	annotateGrader string
	annotateFrom   string
	annotateStrict bool
)

// scoreInput is one BYO-grader verdict, the unit of `qvr audit annotate --from`.
// session is the agent-native id the runner held (e.g. claude --session-id);
// skill is the run's skill the grade judges (required — it scopes the grade so a
// multi-skill session's grade is not double-counted); agent/metric default when
// empty.
type scoreInput struct {
	Session string  `json:"session"`
	Agent   string  `json:"agent"`
	Skill   string  `json:"skill"`
	Metric  string  `json:"metric"`
	Score   float64 `json:"score"`
	Grader  string  `json:"grader"`
}

var auditAnnotateCmd = &cobra.Command{
	Use:   "annotate <agent-session-id>",
	Short: "Attach a BYO-grader quality score to a run",
	Long: `Record an external grader's verdict for one SKILL's run, keyed by the
AGENT-NATIVE session id the runner already holds (e.g. the id you passed to
'claude --session-id', or read back from '--output-format json') and the --skill
the grade judges.

The grade asserts "this skill's run scored X", not "this session scored X": a
session can load several skills, so --skill scopes the grade to the one it judged
and keeps a multi-skill session's grade from double-counting across the others.

qvr stores the number; the grader (exact/regex/LLM-judge — anything) runs outside
qvr and is none of its business. The score is a blind write: it does NOT require
the session to be discovered yet, so you can grade immediately after a run and the
score is picked up whenever the session is ingested (grade-first, discover-later).
It folds into 'qvr audit compare' as a per-cohort mean (pass-rate), attributed to
whichever content version the run actually loaded. With --strict, a grade whose
already-discovered run never loaded --skill is refused (without it, just warned).

  # one run
  qvr audit annotate DCE3…701F --skill triage --metric accuracy --score 1.0 --grader exact

  # a batch from a grader, one JSON object per line:
  #   {"session":"…","agent":"claude","skill":"triage","metric":"accuracy","score":1.0,"grader":"exact"}
  qvr audit annotate --from scores.jsonl     # or --from - for stdin`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAuditAnnotate,
}

func init() {
	f := auditAnnotateCmd.Flags()
	f.Float64Var(&annotateScore, "score", 0, "the grade (1=pass, 0=fail, or any number)")
	f.StringVar(&annotateAgent, "agent", "claude", "agent that ran the session (matches the capture target)")
	f.StringVar(&annotateSkill, "skill", "", "the run's skill this grade judges (required; scopes the grade)")
	f.StringVar(&annotateMetric, "metric", "score", "metric name (e.g. accuracy); a run may carry several")
	f.StringVar(&annotateGrader, "grader", "", "grader provenance, recorded as-is (e.g. exact, regex, llm:opus)")
	f.StringVar(&annotateFrom, "from", "", "read score objects as JSONL from a file, or - for stdin")
	f.BoolVar(&annotateStrict, "strict", false, "refuse a grade whose discovered run never loaded --skill (else warn)")
	auditCmd.AddCommand(auditAnnotateCmd)
}

func runAuditAnnotate(cmd *cobra.Command, args []string) error {
	inputs, err := collectScoreInputs(cmd, args)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return fmt.Errorf("nothing to record: pass <session-id> --score, or --from <file>")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Write mode: opening the store creates the DB and applies migrations if
	// needed, so grade-first works even before any session is captured.
	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	// Guard every input BEFORE writing any, so a --strict rejection mid-batch
	// leaves no partial grades behind (in the default mode this only warns).
	for _, in := range inputs {
		if err := verifySkillLoaded(cmd.Context(), s, in); err != nil {
			return err
		}
	}
	for _, in := range inputs {
		if err := s.PutScore(cmd.Context(), in.Agent, in.Session, in.Skill, in.Metric, in.Score, in.Grader); err != nil {
			return fmt.Errorf("record score for %q: %w", in.Session, err)
		}
	}
	if outputFormat == "json" {
		return printer.JSON(map[string]int{"recorded": len(inputs)})
	}
	printer.Info(fmt.Sprintf("Recorded %d score(s)", len(inputs)))
	return nil
}

// verifySkillLoaded enforces annotate's (skill, session) contract against an
// already-discovered run: a grade naming a skill the run never loaded cannot
// attribute (the content rollup's skill join drops it), so it is flagged. An
// undiscovered run is left alone — grade-first/discover-later stays a blind write,
// and the grade attaches once the session is ingested. --strict turns the flag
// into a hard refusal.
func verifySkillLoaded(ctx context.Context, s store.Store, in scoreInput) error {
	known, loaded, err := s.SessionSkillLoaded(ctx, in.Agent, in.Session, in.Skill)
	if err != nil {
		return err
	}
	if !known || loaded {
		return nil
	}
	msg := fmt.Sprintf("session %s never loaded skill %q — its grade will not attribute", in.Session, in.Skill)
	if annotateStrict {
		return fmt.Errorf("%s; pass the skill the run actually loaded, or drop --strict to record anyway", msg)
	}
	printer.Warning(msg)
	return nil
}

// collectScoreInputs gathers the verdicts to write, from either the batch --from
// stream or a single positional session + flags, applying the agent/metric
// defaults to each.
func collectScoreInputs(cmd *cobra.Command, args []string) ([]scoreInput, error) {
	if annotateFrom != "" {
		if len(args) > 0 {
			return nil, fmt.Errorf("pass either <session-id> or --from, not both")
		}
		return readScoreBatch(cmd, annotateFrom)
	}
	if len(args) == 0 {
		return nil, nil
	}
	if !cmd.Flags().Changed("score") {
		return nil, fmt.Errorf("--score is required (the grade to record)")
	}
	if strings.TrimSpace(annotateSkill) == "" {
		return nil, fmt.Errorf("--skill is required (the run's skill this grade judges)")
	}
	return []scoreInput{normalizeScore(scoreInput{
		Session: args[0], Agent: annotateAgent, Skill: annotateSkill, Metric: annotateMetric,
		Score: annotateScore, Grader: annotateGrader,
	})}, nil
}

// readScoreBatch parses JSONL score objects from path ("-" = stdin).
func readScoreBatch(cmd *cobra.Command, path string) ([]scoreInput, error) {
	r := cmd.InOrStdin()
	if path != "-" {
		fh, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open --from %q: %w", path, err)
		}
		defer fh.Close()
		r = fh
	}
	var out []scoreInput
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var in scoreInput
		if err := json.Unmarshal([]byte(text), &in); err != nil {
			return nil, fmt.Errorf("--from line %d: %w", line, err)
		}
		if in.Session == "" {
			return nil, fmt.Errorf("--from line %d: missing \"session\"", line)
		}
		if strings.TrimSpace(in.Skill) == "" {
			return nil, fmt.Errorf("--from line %d: missing \"skill\" (the run's skill this grade judges)", line)
		}
		out = append(out, normalizeScore(in))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read --from: %w", err)
	}
	return out, nil
}

// normalizeScore applies the agent/metric defaults a bare score object may omit.
// A batch item with no "agent" inherits the --agent target (not a hardcoded
// default), so a `--from` file graded against a non-claude capture is attributed
// correctly instead of silently landing under "claude".
func normalizeScore(in scoreInput) scoreInput {
	if in.Agent == "" {
		in.Agent = annotateAgent
	}
	if in.Metric == "" {
		in.Metric = "score"
	}
	return in
}
