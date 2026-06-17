package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/eval"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/spf13/cobra"
)

// opsCmd is the parent for SkillOps — evaluating skills as first-class
// artifacts and gating their evolution on evidence. It builds on the audit
// trace layer: `qvr ops eval` grades a skill's captured sessions against its
// evals.yaml, and `qvr ops lineage` joins those verdicts to the spans by the
// exact locked commit that ran.
var opsCmd = &cobra.Command{
	Use:   "ops",
	Short: "[experimental] Evaluate skills and gate their evolution on evidence",
	Long: `[EXPERIMENTAL] SkillOps treats a skill as a first-class artifact with an
evals.yaml manifest (a sibling of SKILL.md). It grades the skill's CAPTURED
sessions deterministically — pure Go over the spans qvr already derived, no
model calls, no agent execution — and keys every verdict to the exact locked
commit that ran. That is the evidence a self-improvement loop gates on: improve
the skill, re-run it, and 'qvr ops eval' must show the score improve before the
change ships.

Subcommands: eval (grade a skill), lineage (verdicts over commits), promote
(refuse to advance a skill without a passing eval).`,
	RunE: rejectUnknownSubcommand,
}

func init() {
	rootCmd.AddCommand(opsCmd)
}

// resolvedSkill carries everything the ops subcommands need about a target
// skill: where its evals.yaml/SKILL.md live, and the locked commit that pins
// every eval verdict (the lock × evidence join). Commit is "" for a skill with
// no project lock entry (e.g. a local fixture not yet committed).
type resolvedSkill struct {
	Name   string
	Dir    string
	Commit string
}

// resolveSkill locates a skill's directory and locked commit. An explicit
// --skill-dir wins (the evolve loop points it at the ejected, writable copy);
// otherwise the installed project target dir is searched. The commit is read
// from the project lock when present.
func resolveSkill(name, skillDirFlag string) (*resolvedSkill, error) {
	projectRoot, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve cwd: %w", err)
	}
	out := &resolvedSkill{Name: name}

	if skillDirFlag != "" {
		out.Dir = skillDirFlag
	} else {
		dir, ok := installedSkillDir(projectRoot, name)
		if !ok {
			return nil, fmt.Errorf("could not find skill %q under any target dir — pass --skill-dir <path>", name)
		}
		out.Dir = dir
	}
	if _, err := os.Stat(filepath.Join(out.Dir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("%s has no SKILL.md: %w", out.Dir, err)
	}
	out.Commit = lockedCommit(projectRoot, name)
	return out, nil
}

// installedSkillDir searches the distinct project-relative target dirs for an
// installed skill named name, returning the first that holds a SKILL.md. The
// dirs are searched in sorted order so resolution is deterministic.
func installedSkillDir(projectRoot, name string) (string, bool) {
	seen := map[string]bool{}
	var dirs []string
	for _, t := range model.Targets {
		if !seen[t.LocalDir] {
			seen[t.LocalDir] = true
			dirs = append(dirs, t.LocalDir)
		}
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		cand := filepath.Join(projectRoot, d, name)
		if _, err := os.Stat(filepath.Join(cand, "SKILL.md")); err == nil {
			return cand, true
		}
	}
	return "", false
}

// lockedCommit reads the project lock for the skill's pinned commit, or "" when
// there is no lock/entry (best-effort: a missing commit just leaves the eval
// run keyed by an empty commit, still time-ordered for lineage).
func lockedCommit(projectRoot, name string) string {
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), false)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return ""
	}
	if e, ok := lock.Skills[name]; ok {
		return e.Commit
	}
	return ""
}

// loadEvalSuiteNames is a small helper for diagnostics: the suite names a
// skill's manifest defines, or nil.
func loadEvalSuiteNames(dir string) []string {
	man, err := eval.Load(dir)
	if err != nil || man == nil {
		return nil
	}
	names := make([]string, 0, len(man.Suites))
	for _, s := range man.Suites {
		names = append(names, s.Name)
	}
	return names
}
