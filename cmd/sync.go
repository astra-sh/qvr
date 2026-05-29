package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	syncGlobal        bool
	syncDryRun        bool
	syncKeepUntracked bool
	syncNoScan        bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Reconcile the project against qvr.lock",
	Long: `Make the on-disk state match the lock file. For every entry in the
lock, ensure its worktree exists in the shared cache and the agent-target
symlinks point at it. Then strict-remove any symlinks under managed agent
directories (.claude/skills/, .cursor/rules/, etc.) whose target is a
qvr-managed cache path but which don't appear in the lock — that's the
"hidden by default" guarantee.

A symlink whose target sits outside the qvr-managed scope (e.g. into your
own dev directory or somewhere weirder like /etc/passwd) is left alone
and surfaced in the output so you can investigate; sync never removes
anything we don't recognise as ours.

Pass --global to reconcile against the user-global lock at ~/.quiver/qvr.lock.
Pass --dry-run to see what would change without touching the filesystem.
Pass --keep-untracked to downgrade orphan removal to a warning — handy
when you mix hand-managed skills with qvr-managed ones in the same dir.`,
	RunE: runSync,
}

func init() {
	syncCmd.Flags().BoolVar(&syncGlobal, "global", false,
		"reconcile against the user-global lock instead of the project lock")
	syncCmd.Flags().BoolVar(&syncDryRun, "dry-run", false,
		"report what would change without touching the filesystem")
	syncCmd.Flags().BoolVar(&syncKeepUntracked, "keep-untracked", false,
		"warn about orphan managed symlinks instead of removing them")
	syncCmd.Flags().BoolVar(&syncNoScan, "no-scan", false,
		"skip the per-skill security scan that normally surfaces issues found in restored worktrees")
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), syncGlobal)

	var (
		result     *skill.ReconcileResult
		latestLock *model.LockFile
	)
	lockErr := model.WithLock(lockPath, func() error {
		lock, err := model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}

		gc := git.NewGoGitClient()
		wt := git.NewGoGitWorktree()
		installer := skill.NewInstaller(registry.NewManager(gc), wt, gc)
		reconciler := skill.NewReconciler(installer)

		r, err := reconciler.Reconcile(lock, projectRoot, config.Dir(), skill.ReconcileOptions{
			DryRun:        syncDryRun,
			KeepUntracked: syncKeepUntracked,
		})
		if err != nil {
			return fmt.Errorf("sync: %w", err)
		}
		result = r
		latestLock = lock
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	registry.TouchProject(lockPath)

	// Refresh AGENTS.md if the user has opted in (file already present). The
	// reconciler may have changed which skills are visible, so the doc cache
	// can otherwise lie until the next manual `qvr docs`.
	if !syncGlobal && !syncDryRun && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}

	// Security gate. Sync re-materialises worktrees from the lock; we rescan
	// each restored skill so a registry that turned hostile between add and
	// sync gets flagged. Sync intentionally only surfaces findings and does
	// not roll back — the lock already committed to these refs and the user
	// can `qvr remove` individually after reviewing what the scan said.
	if !syncDryRun && latestLock != nil {
		cfg, cerr := config.Load()
		if cerr == nil {
			scanRestoredSkillsAfterSync(cmd.Context(), latestLock, cfg)
		}
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}

	for _, name := range result.Installed {
		printer.Success(fmt.Sprintf("Restored %s", name))
	}
	for _, path := range result.SymlinksFixed {
		printer.Info(fmt.Sprintf("Linked %s", path))
	}
	for _, path := range result.Removed {
		printer.Warning(fmt.Sprintf("Removed orphan %s", path))
	}
	for _, skipped := range result.Skipped {
		printer.Info(fmt.Sprintf("Skipped %s", skipped))
	}
	for _, e := range result.Errors {
		printer.Error(e)
	}
	if len(result.Installed)+len(result.SymlinksFixed)+len(result.Removed) == 0 && len(result.Errors) == 0 {
		printer.Success("Already in sync.")
	}
	return nil
}

// scanRestoredSkillsAfterSync runs the standard scan gate against every
// installed (non-link, non-disabled) entry in lock and surfaces findings.
// Sync is restorative — the lock already committed to these refs — so a
// blocked finding only WARNS rather than rolling back. The user can act on
// the surfaced findings with `qvr remove <name>` or `qvr switch <name> <ref>`
// to a safer version.
func scanRestoredSkillsAfterSync(ctx context.Context, lock *model.LockFile, cfg *config.Config) {
	if !gateAvailable(cfg, syncNoScan) {
		return
	}
	for _, entry := range lock.Entries() {
		if entry.Disabled || entry.Source == "link" {
			continue
		}
		skillDir := skill.EffectiveTarget(entry)
		if skillDir == "" {
			continue
		}
		// We pass `Action: "sync"` so the surfaced banner makes it clear
		// these findings came from the rescan, not an add.
		_, _ = ScanAndGate(ctx, skillDir, cfg, scanGateOptions{
			Action:  "sync",
			Subject: entry.Name,
		})
	}
}
