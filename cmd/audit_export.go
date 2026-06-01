package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/ops"
	"github.com/raks097/quiver/internal/ops/store"
	"github.com/spf13/cobra"
)

var (
	exportSkill string
	exportAgent string
	exportSince string
	exportOut   string
)

var auditExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export recorded events as canonical JSONL",
	Long: `Streams matching events as one canonical JSON object per line (the
$schema-stamped Event format), to stdout or a file. Suitable for archival,
external analysis, or 'ingest' into another Quiver store.`,
	Args: cobra.NoArgs,
	RunE: runAuditExport,
}

func init() {
	f := auditExportCmd.Flags()
	f.StringVar(&exportSkill, "skill", "", "filter by skill name")
	f.StringVar(&exportAgent, "agent", "", "filter by agent name")
	f.StringVar(&exportSince, "since", "", "only events since this time (e.g. 7d, 24h, or RFC3339)")
	f.StringVarP(&exportOut, "out", "o", "", "write to this file instead of stdout")
	auditCmd.AddCommand(auditExportCmd)
}

func runAuditExport(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	filter, err := buildExportFilter()
	if err != nil {
		return err
	}

	// Destination writer.
	var w *bufio.Writer
	if exportOut != "" {
		f, oErr := os.Create(exportOut)
		if oErr != nil {
			return fmt.Errorf("create %s: %w", exportOut, oErr)
		}
		defer f.Close()
		w = bufio.NewWriter(f)
	} else {
		w = bufio.NewWriter(os.Stdout)
	}
	defer w.Flush()

	if !auditDBExists(cfg) {
		// Nothing to export — succeed with zero output.
		if exportOut != "" {
			printer.Info("no events to export")
		}
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	count := 0
	err = s.StreamEvents(cmd.Context(), filter, func(e *ops.Event) error {
		// Event.MarshalJSON emits the $schema-stamped canonical shape.
		data, mErr := json.Marshal(e)
		if mErr != nil {
			return mErr
		}
		if _, wErr := w.Write(data); wErr != nil {
			return wErr
		}
		if _, wErr := w.WriteString("\n"); wErr != nil {
			return wErr
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("export events: %w", err)
	}

	if exportOut != "" {
		printer.Success(fmt.Sprintf("exported %d events to %s", count, exportOut))
	}
	return nil
}

func buildExportFilter() (*store.EventFilter, error) {
	f := &store.EventFilter{}
	if exportSkill != "" {
		f.Skills = []string{exportSkill}
	}
	if exportAgent != "" {
		f.Agents = []string{exportAgent}
	}
	if exportSince != "" {
		t, err := parseTimeFlag(exportSince)
		if err != nil {
			return nil, fmt.Errorf("invalid --since: %w", err)
		}
		f.Since = &t
	}
	return f, nil
}
