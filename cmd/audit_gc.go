package cmd

import (
	"fmt"
	"time"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/ops"
	"github.com/spf13/cobra"
)

var gcOlderThan string

var auditGCCmd = &cobra.Command{
	Use:   "gc",
	Short: "Sweep recorded sessions that never referenced a skill",
	Long: `Discard recorded sessions that never referenced any skill and have been
idle past a cutoff. Skill-less sessions are retained by default (the audit
trail is capture-first), so this is the manual sweep for reclaiming them; set
ops.prune_skill_less_sessions to discard them automatically at session end
instead.`,
	Args: cobra.NoArgs,
	RunE: runAuditGC,
}

func init() {
	auditGCCmd.Flags().StringVar(&gcOlderThan, "older-than", "",
		"only sweep sessions started before this age (e.g. 24h, 7d; default 24h)")
	auditCmd.AddCommand(auditGCCmd)
}

func runAuditGC(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	cutoff := time.Now().UTC().Add(-ops.DefaultSkilllessSweep)
	if gcOlderThan != "" {
		t, perr := parseTimeFlag(gcOlderThan)
		if perr != nil {
			return fmt.Errorf("invalid --older-than: %w", perr)
		}
		cutoff = t
	}

	// Nothing recorded yet — treat as a no-op rather than failing a
	// read-only open on a missing DB.
	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON(map[string]any{"sessions_swept": 0})
		}
		printer.Info("nothing to sweep")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	n, err := s.DeleteSkilllessSessions(cmd.Context(), cutoff)
	if err != nil {
		return fmt.Errorf("sweep skill-less sessions: %w", err)
	}

	if outputFormat == "json" {
		return printer.JSON(map[string]any{"sessions_swept": n})
	}
	printer.Success(fmt.Sprintf("swept %d skill-less session(s)", n))
	return nil
}
