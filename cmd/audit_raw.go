package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	rawSession string
	rawAgent   string
	rawSource  string
	rawLimit   int
)

var auditRawCmd = &cobra.Command{
	Use:    "raw",
	Hidden: true, // low-level plumbing — see `qvr audit --help`
	Short:  "Print captured traces exactly as the agent produced them",
	Long: `Emit the raw, verbatim trace rows — the agent's own transcript lines and
hook payloads, byte-for-byte, with no parsing or normalization. In text mode the
native JSONL is reproduced line by line (so you get back exactly what the agent
wrote); --output json wraps each row with its capture metadata.`,
	Args: cobra.NoArgs,
	RunE: runAuditRaw,
}

var auditSpansCmd = &cobra.Command{
	Use:    "spans",
	Hidden: true, // low-level plumbing — see `qvr audit --help`
	Short:  "Derive OpenTelemetry spans from captured raw traces",
	Long: `Project the raw traces of a session into OpenTelemetry spans
(Turn / Tool / Skill), using gen_ai.* semantic conventions plus a skill.name
tag. This is a regenerable view over the raw bytes — it never re-captures. Use
--output json for the span list, or --otlp for an OTLP payload ready to POST to
any OTLP consumer.`,
	Args: cobra.NoArgs,
	RunE: runAuditSpans,
}

var spansOTLP bool

func init() {
	rf := auditRawCmd.Flags()
	rf.StringVar(&rawSession, "session", "", "filter by canonical session id")
	rf.StringVar(&rawAgent, "agent", "", "filter by agent name")
	rf.StringVar(&rawSource, "source", "", "filter by source (transcript | hook_payload)")
	rf.IntVar(&rawLimit, "limit", 0, "maximum rows (0 = no limit)")
	auditCmd.AddCommand(auditRawCmd)

	sf := auditSpansCmd.Flags()
	sf.StringVar(&rawSession, "session", "", "session id to derive (required unless --agent)")
	sf.StringVar(&rawAgent, "agent", "", "agent name (derive every captured session for this agent)")
	sf.BoolVar(&spansOTLP, "otlp", false, "emit an OTLP resourceSpans payload")
	auditCmd.AddCommand(auditSpansCmd)
}

func rawFilter() (*store.RawTraceFilter, error) {
	f := &store.RawTraceFilter{Limit: rawLimit}
	if rawAgent != "" {
		f.Agents = []string{rawAgent}
	}
	if rawSource != "" {
		f.Sources = []string{rawSource}
	}
	if rawSession != "" {
		id, err := uuid.Parse(rawSession)
		if err != nil {
			return nil, fmt.Errorf("invalid --session id %q: %w", rawSession, err)
		}
		f.SessionID = &id
	}
	return f, nil
}

func runAuditRaw(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	filter, err := rawFilter()
	if err != nil {
		return err
	}
	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]any{})
		}
		printer.Info("No traces recorded yet")
		return nil
	}
	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	rows, err := s.QueryRawTraces(cmd.Context(), filter)
	if err != nil {
		return fmt.Errorf("query raw traces: %w", err)
	}

	if outputFormat == "json" {
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{
				"agent_name":  r.AgentName,
				"session_id":  r.SessionID.String(),
				"seq":         r.Seq,
				"source":      r.Source,
				"hook_type":   r.HookType,
				"source_path": r.SourcePath,
				"captured_at": r.CapturedAt,
				"raw":         json.RawMessage(r.Raw), // emit native JSON inline
			})
		}
		return printer.JSON(out)
	}

	// Text mode: reproduce the verbatim native bytes, one row per line.
	w := cmd.OutOrStdout()
	for _, r := range rows {
		fmt.Fprintln(w, string(r.Raw))
	}
	if len(rows) == 0 {
		printer.Info("No traces match")
	}
	return nil
}

func runAuditSpans(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	f, err := spanFilter()
	if err != nil {
		return err
	}
	if f.SessionID == nil && rawAgent == "" {
		return fmt.Errorf("specify --session <id> or --agent <name>")
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]derive.Span{})
		}
		printer.Info("No traces recorded yet")
		return nil
	}
	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	spans, err := storedDerivedSpans(cmd.Context(), s, f)
	if err != nil {
		return err
	}

	if spansOTLP {
		return printer.JSON(derive.ToOTLP(spans))
	}
	if outputFormat == "json" {
		return printer.JSON(spans)
	}
	if len(spans) == 0 {
		printer.Info("No spans for this scope — run `qvr audit discover` to populate (or `qvr audit rederive` to refresh), and `--raw` for the captured transcript")
		return nil
	}
	headers := []string{"KIND", "NAME", "MODEL", "TOKENS", "SKILL"}
	tableRows := make([][]string, 0, len(spans))
	for _, sp := range spans {
		tableRows = append(tableRows, []string{
			sp.Kind,
			sp.Name,
			attrString(sp.Attributes, "gen_ai.request.model"),
			attrTokens(sp.Attributes),
			attrString(sp.Attributes, "skill.name"),
		})
	}
	printer.Table(headers, tableRows)
	return nil
}

// spanFilter builds the stored-span query from the shared session/agent flags.
func spanFilter() (*store.SpanFilter, error) {
	f := &store.SpanFilter{Limit: rawLimit}
	if rawAgent != "" {
		f.Agents = []string{rawAgent}
	}
	if rawSession != "" {
		id, err := uuid.Parse(rawSession)
		if err != nil {
			return nil, fmt.Errorf("invalid --session id %q: %w", rawSession, err)
		}
		f.SessionID = &id
	}
	return f, nil
}

// storedDerivedSpans reads the PERSISTED span projection (it never re-derives),
// so export/spans show the identity FROZEN at ingest — the version that actually
// ran — rather than a label re-resolved against whatever happens to be checked
// out now (persist-and-trust). Stale spans (an older deriver) refresh with
// `qvr audit rederive`.
func storedDerivedSpans(ctx context.Context, s store.Store, f *store.SpanFilter) ([]derive.Span, error) {
	rows, err := s.QuerySpans(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("query spans: %w", err)
	}
	out := make([]derive.Span, 0, len(rows))
	for _, r := range rows {
		out = append(out, spanRowToDerive(r))
	}
	return out, nil
}

// spanRowToDerive rehydrates a stored SpanRow into a derive.Span (attributes
// parsed from their JSON column) so the same emission/OTLP path serves both
// freshly-derived and persisted spans.
func spanRowToDerive(r *store.SpanRow) derive.Span {
	var attrs map[string]any
	if r.Attributes != "" {
		_ = json.Unmarshal([]byte(r.Attributes), &attrs)
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	return derive.Span{
		Name:         r.Name,
		Kind:         r.Kind,
		SpanID:       r.SpanID,
		TraceID:      r.TraceID,
		ParentSpanID: r.ParentSpanID,
		StartMs:      r.StartMs,
		EndMs:        r.EndMs,
		Attributes:   attrs,
	}
}

func attrString(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func attrTokens(m map[string]any) string {
	if n := attrInt(m, "gen_ai.usage.input_tokens") + attrInt(m, "gen_ai.usage.output_tokens"); n > 0 {
		return strconv.Itoa(n)
	}
	return ""
}

// attrInt reads a numeric attribute as an int, tolerating the float64 that
// arrives when attributes are rehydrated from stored JSON.
func attrInt(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
