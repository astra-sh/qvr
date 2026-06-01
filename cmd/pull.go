package cmd

import (
	"errors"
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

var pullGlobal bool

var pullCmd = &cobra.Command{
	Use:   "pull [skill]...",
	Short: "Pull upstream changes into installed skills",
	Long: `Fetch and fast-forward the worktree for one or more skills. When called
without arguments, pulls every skill in the lock file.

Pull refuses to clobber local work: a dirty worktree or a diverged branch is
reported as a conflict for the user to resolve manually.`,
	RunE: runPull,
}

func init() {
	pullCmd.Flags().BoolVar(&pullGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	rootCmd.AddCommand(pullCmd)
}

func runPull(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), pullGlobal)

	var (
		results    []map[string]string
		loopErr    error
		latestLock *model.LockFile
		nothing    bool
		refused    int
	)
	lockErr := model.WithLock(lockPath, func() error {
		lock, err := model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}
		names := args
		if len(names) == 0 {
			for _, e := range lock.Entries() {
				names = append(names, e.Name)
			}
		}
		if len(names) == 0 {
			nothing = true
			return nil
		}

		syncer := skill.NewSyncer(git.NewGoGitWorktree(), git.NewGoGitClient())
		for _, name := range names {
			entry, err := lock.Get(name)
			if err != nil {
				loopErr = fmt.Errorf("%s: %w", name, err)
				break
			}
			hash, err := syncer.Pull(cmd.Context(), entry)
			if err != nil {
				// A diverged or tag-pinned entry is a refusal: the requested pull
				// did not happen. Both are diagnostics (→ stderr, never stdout —
				// stdout stays clean for the JSON payload / `jq`) and both flip
				// the exit code non-zero so a script notices. We still `continue`
				// rather than `break` so the remaining named skills get pulled and
				// no in-progress edits are lost (AC-LIFE-3 / AC-LIFE-4, #129).
				if errors.Is(err, skill.ErrDivergence) {
					printer.Warning(fmt.Sprintf("%s: %v", name, err))
					results = append(results, map[string]string{"name": name, "status": "conflict", "message": err.Error()})
					refused++
					continue
				}
				if errors.Is(err, skill.ErrPinnedToTag) {
					printer.Warning(fmt.Sprintf("%s: %v", name, err))
					results = append(results, map[string]string{"name": name, "status": "skipped", "message": err.Error()})
					refused++
					continue
				}
				loopErr = fmt.Errorf("pull %s: %w", name, err)
				break
			}
			entry.Commit = hash
			_ = skill.RefreshSubtreeHash(entry)
			lock.Put(entry)
			printer.Success(fmt.Sprintf("%s: updated to %s", name, shortHash(hash)))
			results = append(results, map[string]string{"name": name, "status": "updated", "commit": hash})
		}
		if err := lock.Write(); err != nil {
			if loopErr != nil {
				return fmt.Errorf("write lock after %v: %w", loopErr, err)
			}
			return fmt.Errorf("write lock: %w", err)
		}
		latestLock = lock
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	if nothing {
		printer.Info("No installed skills. Run 'qvr add <skill>' first.")
		return nil
	}
	registry.TouchProject(lockPath)
	if !pullGlobal && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}
	if loopErr != nil {
		return loopErr
	}
	if printer.Format == output.FormatJSON {
		if err := printer.JSON(results); err != nil {
			return err
		}
		// The payload already encodes each refusal's status/message; exit
		// non-zero without a second envelope so the stream stays one JSON doc.
		if refused > 0 {
			return errJSONHandled
		}
		return nil
	}
	// Refusals were already printed to stderr per skill; flip the exit code
	// without re-printing (errTextHandled) so a tag-pinned / diverged pull
	// fails loudly instead of exiting 0 (#129).
	if refused > 0 {
		return errTextHandled
	}
	return nil
}
