package cmd

import (
	"fmt"
	"time"

	"github.com/raks097/quiver/internal/config"
	"github.com/spf13/cobra"
)

var gcOlderThan string

// defaultRawRetention is how far back `qvr audit gc` keeps raw traces when no
// --older-than is given.
const defaultRawRetention = 30 * 24 * time.Hour

var auditGCCmd = &cobra.Command{
	Use:   "gc",
	Short: "Prune captured raw traces older than a cutoff",
	Long: `Delete raw trace rows (and their derived spans become stale) captured
before a cutoff, to bound the local database size. Defaults to pruning anything
older than 30 days; override with --older-than.`,
	Args: cobra.NoArgs,
	RunE: runAuditGC,
}

func init() {
	auditGCCmd.Flags().StringVar(&gcOlderThan, "older-than", "",
		"prune traces captured before this age (e.g. 24h, 7d; default 30d)")
	auditCmd.AddCommand(auditGCCmd)
}

func runAuditGC(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	cutoff := time.Now().UTC().Add(-defaultRawRetention)
	if gcOlderThan != "" {
		t, perr := parseTimeFlag(gcOlderThan)
		if perr != nil {
			return fmt.Errorf("invalid --older-than: %w", perr)
		}
		cutoff = t
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON(map[string]any{"traces_pruned": 0})
		}
		printer.Info("nothing to prune")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	n, err := s.DeleteRawBefore(cmd.Context(), cutoff)
	if err != nil {
		return fmt.Errorf("prune raw traces: %w", err)
	}

	if outputFormat == "json" {
		return printer.JSON(map[string]any{"traces_pruned": n})
	}
	printer.Success(fmt.Sprintf("pruned %d raw trace(s)", n))
	return nil
}
