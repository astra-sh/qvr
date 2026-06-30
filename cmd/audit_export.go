package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	exportAgent   string
	exportSession string
	exportSource  string
	exportOut     string
	exportRaw     bool
	exportOTLP    bool
)

var auditExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export derived spans as JSONL (or the raw transcript with --raw)",
	Long: `Streams the derived span tree — the same clean Turn / model / tool /
skill spans qvr shows in the UI — as one span JSON per line. This is the
shareable, normalized view: downstream tools consume spans, not each agent's
private transcript format.

  --raw     emit the agent's verbatim transcript lines + hook payloads instead
            (one native JSON object per line; --source restricts to transcript
            or hook_payload)
  --otlp    emit a single OTLP resourceSpans payload ready to POST to any OTLP
            consumer

Spans are a regenerable projection of the captured raw bytes — exporting them
never re-captures.`,
	Args: cobra.NoArgs,
	RunE: runAuditExport,
}

func init() {
	f := auditExportCmd.Flags()
	f.StringVar(&exportAgent, "agent", "", "filter by agent name")
	f.StringVar(&exportSession, "session", "", "filter by session id")
	f.StringVar(&exportSource, "source", "", "with --raw: filter by source (transcript | hook_payload)")
	f.StringVarP(&exportOut, "out", "o", "", "write to this file instead of stdout")
	f.BoolVar(&exportRaw, "raw", false, "export the verbatim raw transcript instead of derived spans")
	f.BoolVar(&exportOTLP, "otlp", false, "export a single OTLP resourceSpans payload")
	auditCmd.AddCommand(auditExportCmd)
}

// buildExportFilter assembles the raw-trace filter from the export flags,
// validating the --session id when given.
func buildExportFilter() (*store.RawTraceFilter, error) {
	filter := &store.RawTraceFilter{}
	if exportAgent != "" {
		filter.Agents = []string{exportAgent}
	}
	if exportSource != "" {
		filter.Sources = []string{exportSource}
	}
	if exportSession != "" {
		id, perr := uuid.Parse(exportSession)
		if perr != nil {
			return nil, fmt.Errorf("invalid --session id %q: %w", exportSession, perr)
		}
		filter.SessionID = &id
	}
	return filter, nil
}

func runAuditExport(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if exportRaw && exportOTLP {
		return fmt.Errorf("--raw and --otlp are mutually exclusive")
	}

	w, closeFn, err := openExportWriter()
	if err != nil {
		return err
	}
	defer closeFn()

	if !auditDBExists(cfg) {
		if exportOut != "" {
			printer.Info("No traces to export")
		}
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	noun := "span"
	if exportRaw {
		noun = "trace"
	}
	count, err := writeExport(cmd, s, w)
	if err != nil {
		return err
	}
	// Surface any buffered-write error rather than dropping it on a deferred
	// flush — a truncated export must not report success.
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush export: %w", err)
	}
	if exportOut != "" {
		printer.Success(fmt.Sprintf("Exported %s to %s", output.Plural(count, noun), exportOut))
	}
	return nil
}

// openExportWriter returns the buffered writer for the export and a close
// function (a no-op for stdout).
func openExportWriter() (*bufio.Writer, func(), error) {
	if exportOut == "" {
		return bufio.NewWriter(os.Stdout), func() {}, nil
	}
	f, err := os.Create(exportOut)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", exportOut, err)
	}
	return bufio.NewWriter(f), func() { _ = f.Close() }, nil
}

// writeExport dispatches to the chosen export form and returns the row/span
// count written. Default is the derived span tree (the shareable view); --raw
// emits the verbatim transcript; --otlp emits one OTLP payload.
func writeExport(cmd *cobra.Command, s store.Store, w *bufio.Writer) (int, error) {
	if exportRaw {
		return exportRawTraces(cmd, s, w)
	}
	return exportSpans(cmd, s, w)
}

// exportRawTraces streams the verbatim native bytes (transcript lines and hook
// payloads), one per line — the pre-spans behavior, now opt-in behind --raw.
func exportRawTraces(cmd *cobra.Command, s store.Store, w *bufio.Writer) (int, error) {
	filter, err := buildExportFilter()
	if err != nil {
		return 0, err
	}
	count := 0
	err = s.StreamRawTraces(cmd.Context(), filter, func(r *ops.RawTrace) error {
		if _, wErr := w.Write(r.Raw); wErr != nil {
			return wErr
		}
		if _, wErr := w.WriteString("\n"); wErr != nil {
			return wErr
		}
		count++
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("export traces: %w", err)
	}
	return count, nil
}

// exportSpans derives the span tree from the captured transcript rows and emits
// it — one span JSON per line (JSONL), or a single OTLP payload with --otlp.
func exportSpans(cmd *cobra.Command, s store.Store, w *bufio.Writer) (int, error) {
	f := &store.SpanFilter{}
	if exportAgent != "" {
		f.Agents = []string{exportAgent}
	}
	if exportSession != "" {
		id, perr := uuid.Parse(exportSession)
		if perr != nil {
			return 0, fmt.Errorf("invalid --session id %q: %w", exportSession, perr)
		}
		f.SessionID = &id
	}
	// Read the FROZEN projection (persist-and-trust) — never re-resolve labels
	// against the current checkout. Refresh with `qvr audit rederive`.
	spans, err := storedDerivedSpans(cmd.Context(), s, f)
	if err != nil {
		return 0, err
	}
	if exportOTLP {
		payload, mErr := json.Marshal(derive.ToOTLP(spans))
		if mErr != nil {
			return 0, fmt.Errorf("marshal otlp: %w", mErr)
		}
		if _, wErr := w.Write(payload); wErr != nil {
			return 0, wErr
		}
		_, _ = w.WriteString("\n")
		return len(spans), nil
	}
	for _, sp := range spans {
		line, mErr := json.Marshal(sp)
		if mErr != nil {
			return 0, fmt.Errorf("marshal span: %w", mErr)
		}
		if _, wErr := w.Write(line); wErr != nil {
			return 0, wErr
		}
		if _, wErr := w.WriteString("\n"); wErr != nil {
			return 0, wErr
		}
	}
	return len(spans), nil
}
