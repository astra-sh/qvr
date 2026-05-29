package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/spf13/cobra"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Inspect and clean the shared worktree cache",
	Long: `Tools for the shared SHA-keyed worktree cache at ~/.quiver/worktrees/.

  qvr cache list             show reachable + orphan worktrees with sizes
  qvr cache prune --dry-run  show what prune would remove
  qvr cache prune            delete orphan worktrees`,
}

var cachePruneDryRun bool

var cacheListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show reachable and orphan worktrees in the shared cache",
	RunE:  runCacheList,
}

var cachePruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Delete worktrees no longer referenced by any project lock",
	Long: `Walk ~/.quiver/worktrees/, cross-reference against every known project
lock (recorded in projects.json) and the user-global lock, and remove any
worktree directory that no lock entry still references. Also forgets project
entries whose lock files have vanished.

Use --dry-run to see the targets without deleting anything.`,
	RunE: runCachePrune,
}

func init() {
	cachePruneCmd.Flags().BoolVar(&cachePruneDryRun, "dry-run", false,
		"report what would be removed without touching disk")
	cacheCmd.AddCommand(cacheListCmd)
	cacheCmd.AddCommand(cachePruneCmd)
	rootCmd.AddCommand(cacheCmd)
}

// CacheEntry describes one worktree in the cache, used by both list and
// prune output. Reachable is true when the worktree is referenced by at
// least one known lock file.
type CacheEntry struct {
	Path      string `json:"path"`
	Reachable bool   `json:"reachable"`
	SizeBytes int64  `json:"sizeBytes"`
}

// CacheListOutput is the JSON envelope for `qvr cache list`.
type CacheListOutput struct {
	Entries         []CacheEntry `json:"entries"`
	TotalBytes      int64        `json:"totalBytes"`
	OrphanBytes     int64        `json:"orphanBytes"`
	MissingProjects []string     `json:"missingProjects,omitempty"`
}

// CachePruneOutput is the JSON envelope for `qvr cache prune`.
type CachePruneOutput struct {
	Removed         []string `json:"removed"`
	ForgottenProjs  []string `json:"forgottenProjects,omitempty"`
	FreedBytes      int64    `json:"freedBytes"`
	DryRun          bool     `json:"dryRun"`
	MissingProjects []string `json:"missingProjects,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

func runCacheList(cmd *cobra.Command, args []string) error {
	_ = args
	entries, missing, err := collectCacheEntries()
	if err != nil {
		return err
	}
	out := CacheListOutput{
		Entries:         entries,
		MissingProjects: missing,
	}
	for _, e := range entries {
		out.TotalBytes += e.SizeBytes
		if !e.Reachable {
			out.OrphanBytes += e.SizeBytes
		}
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(out)
	}
	if len(entries) == 0 {
		printer.Info("No worktrees in cache.")
		return nil
	}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		state := "reachable"
		if !e.Reachable {
			state = "ORPHAN"
		}
		rows = append(rows, []string{state, humanBytes(e.SizeBytes), shortenCachePath(e.Path)})
	}
	printer.Table([]string{"STATE", "SIZE", "PATH"}, rows)
	printer.Info(fmt.Sprintf("Total: %s (%s orphan)", humanBytes(out.TotalBytes), humanBytes(out.OrphanBytes)))
	for _, miss := range missing {
		printer.Warning(fmt.Sprintf("project lock vanished: %s (run `qvr cache prune` to forget)", miss))
	}
	return nil
}

func runCachePrune(cmd *cobra.Command, args []string) error {
	_ = args
	entries, missing, err := collectCacheEntries()
	if err != nil {
		return err
	}
	out := CachePruneOutput{
		DryRun:          cachePruneDryRun,
		MissingProjects: missing,
	}
	for _, e := range entries {
		if e.Reachable {
			continue
		}
		// In dry-run we record the would-remove without touching disk.
		// In real-run we only count Removed + FreedBytes when the delete
		// actually succeeded — otherwise FreedBytes would lie and CI
		// scripts couldn't tell whether their pruning attempt worked.
		if cachePruneDryRun {
			out.Removed = append(out.Removed, e.Path)
			out.FreedBytes += e.SizeBytes
			continue
		}
		if err := os.RemoveAll(e.Path); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("remove %s: %v", e.Path, err))
			continue
		}
		out.Removed = append(out.Removed, e.Path)
		out.FreedBytes += e.SizeBytes
	}
	if !cachePruneDryRun {
		for _, m := range missing {
			registry.ForgetProject(m)
			out.ForgottenProjs = append(out.ForgottenProjs, m)
		}
	}

	if printer.Format == output.FormatJSON {
		if jerr := printer.JSON(out); jerr != nil {
			return jerr
		}
		if len(out.Errors) > 0 {
			// The body already carries the error list; suppress Execute()'s
			// {"error": "..."} envelope so stdout stays a single JSON doc
			// while exit code still signals failure.
			return errJSONHandled
		}
		return nil
	}
	if len(out.Removed) == 0 {
		printer.Info("Nothing to prune.")
	} else {
		verb := "Removed"
		if cachePruneDryRun {
			verb = "Would remove"
		}
		for _, p := range out.Removed {
			printer.Info(fmt.Sprintf("%s %s", verb, shortenCachePath(p)))
		}
		printer.Success(fmt.Sprintf("%s %d worktree(s), freeing %s",
			verb, len(out.Removed), humanBytes(out.FreedBytes)))
	}
	if !cachePruneDryRun {
		for _, m := range out.ForgottenProjs {
			printer.Info(fmt.Sprintf("Forgot vanished project %s", m))
		}
	}
	for _, e := range out.Errors {
		printer.Error(e)
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("cache prune: %d removal(s) failed", len(out.Errors))
	}
	return nil
}

// collectCacheEntries walks the worktrees root and joins each leaf against the
// reachability set computed from every known project lock plus the global lock.
// Leaves are detected as "the directory contains either a .git directory or is
// already a git working tree" — matches what the installer creates.
func collectCacheEntries() ([]CacheEntry, []string, error) {
	root := registry.WorktreesRoot()
	reach, err := registry.Reachable()
	if err != nil {
		// A reachability read failure means nothing is "reachable" — every
		// worktree would be pruned. Refuse rather than silently nuke the
		// cache.
		return nil, nil, fmt.Errorf("compute reachability: %w", err)
	}

	var entries []CacheEntry
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		return entries, reach.MissingProjects, nil
	}

	// Each leaf worktree is identified by the presence of a .git entry —
	// either a directory (go-git PlainClone) or a file (git worktree-style
	// pointer). Walk only as deep as the first .git hit per branch.
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable subtrees
		}
		if !d.IsDir() {
			return nil
		}
		gitMarker := filepath.Join(path, ".git")
		if _, err := os.Stat(gitMarker); err != nil {
			return nil
		}
		size, _ := dirSize(path)
		_, reachable := reach.Worktrees[path]
		entries = append(entries, CacheEntry{
			Path:      path,
			Reachable: reachable,
			SizeBytes: size,
		})
		return filepath.SkipDir // don't recurse into a worktree
	})
	if err != nil {
		return entries, reach.MissingProjects, fmt.Errorf("walk worktrees root: %w", err)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		// Orphans first so the most useful output is at the top of the table.
		if entries[i].Reachable != entries[j].Reachable {
			return !entries[i].Reachable
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, reach.MissingProjects, nil
}

// dirSize sums the on-disk size of every regular file under dir. Best-effort
// — unreadable files contribute 0 rather than aborting.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// shortenCachePath drops the QUIVER_HOME prefix so the printed table stays
// readable. ~/.quiver/worktrees/foo/bar/sha → worktrees/foo/bar/sha.
func shortenCachePath(p string) string {
	root := registry.WorktreesRoot()
	if strings.HasPrefix(p, root) {
		return "worktrees" + strings.TrimPrefix(p, root)
	}
	return p
}

// humanBytes renders an int64 byte count as a short human-readable string.
// Resolution is intentionally coarse — we want "12 MB" not "12.345 MB".
func humanBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%d GB", b/GB)
	case b >= MB:
		return fmt.Sprintf("%d MB", b/MB)
	case b >= KB:
		return fmt.Sprintf("%d KB", b/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
