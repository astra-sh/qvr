package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/security"
	"github.com/raks097/quiver/internal/skill"
)

// scanGateOptions tunes a single ScanAndGate call.
type scanGateOptions struct {
	// Disabled forces the gate off regardless of cfg.Security.ScanOnInstall.
	// Set by `--no-scan` flags on add/registry/sync/publish.
	Disabled bool
	// Action labels the calling operation in surfaced output ("add", "registry
	// add", "sync restore", "publish"). Used in the rendered banner so the
	// user knows which command produced the findings.
	Action string
	// Subject is the skill name for the banner — e.g. "code-review".
	Subject string
}

// scanGateResult is the outcome of a single ScanAndGate call. Blocked is true
// when the scan ran and at least one finding meets/exceeds the configured
// block_severity threshold. Skipped is true when the gate did not run at all
// (disabled, scan_on_install=false, no skill loadable, etc.) — callers
// distinguish "scan ran clean" from "scan didn't happen" via this field.
type scanGateResult struct {
	Result    *security.ScanResult `json:"result,omitempty"`
	Blocked   bool                 `json:"blocked"`
	Skipped   bool                 `json:"skipped"`
	Threshold security.Severity    `json:"threshold,omitempty"`
}

// ScanAndGate runs the standard scanner against the skill at skillDir and
// applies the cfg.Security.BlockSeverity threshold. Findings are surfaced to
// stderr in text mode (regardless of the global --output) so users always see
// what was flagged, even when the command itself returns a JSON payload.
//
// Returns (result, error). When blocked is true the caller should refuse the
// operation; the surface already happened, so callers should not re-print
// findings.
//
// A nil cfg is treated as the zero SecurityConfig (no scan, no block). When
// opts.Disabled is true the gate is skipped entirely and the returned result
// has Skipped=true with no findings — used for the user-facing `--no-scan`
// path on add/registry/sync/publish.
func ScanAndGate(ctx context.Context, skillDir string, cfg *config.Config, opts scanGateOptions) (*scanGateResult, error) {
	out := &scanGateResult{Skipped: true}
	if opts.Disabled {
		return out, nil
	}
	if cfg == nil || !cfg.Security.ScanOnInstall {
		return out, nil
	}

	loaded, err := skill.LoadFromPath(skillDir)
	if err != nil {
		// A skill that won't load is reported by the validator elsewhere
		// (Install runs validateStagedSkill first). Returning skipped here
		// keeps the gate scoped to security findings — load failures are
		// not the gate's job to surface.
		return out, nil
	}

	threshold, perr := security.ParseSeverity(blockSeverityOrDefault(cfg))
	if perr != nil {
		// Misconfigured block_severity falls back to the safest setting
		// — `critical`. Better to err toward not blocking on bogus input
		// than to refuse every install over a config typo.
		threshold = security.SeverityCritical
	}
	out.Threshold = threshold

	scanner := security.New()
	if p := security.LLMProviderFromEnv(); p != nil {
		scanner = scanner.WithLLMProvider(p)
		for _, lc := range security.BuiltinLLMChecks() {
			scanner = scanner.AddLLM(lc)
		}
	}
	res, err := scanner.Scan(ctx, loaded, skillDir)
	if err != nil {
		return out, fmt.Errorf("scan %s: %w", opts.Subject, err)
	}
	out.Result = res
	out.Skipped = false
	out.Blocked = exceedsThreshold(res, threshold)

	renderGateFindings(opts, res, threshold, out.Blocked)
	return out, nil
}

// blockSeverityOrDefault returns the configured block severity, falling back
// to "critical" when unset.
func blockSeverityOrDefault(cfg *config.Config) string {
	if cfg != nil && cfg.Security.BlockSeverity != "" {
		return cfg.Security.BlockSeverity
	}
	return string(security.SeverityCritical)
}

// renderGateFindings prints a compact, human-readable summary of any scan
// findings to stderr. We deliberately render to stderr — never stdout —
// because callers may be in JSON mode and stdout is reserved for the
// structured payload.
//
// Clean scans are silent so successful installs read normally. Any finding
// triggers a banner, a table of findings, and (if blocked) a "refusing to
// proceed" hint with the --no-scan escape hatch.
func renderGateFindings(opts scanGateOptions, res *security.ScanResult, threshold security.Severity, blocked bool) {
	if res == nil || len(res.Findings) == 0 {
		return
	}
	action := opts.Action
	if action == "" {
		action = "scan"
	}
	subject := opts.Subject
	if subject == "" {
		subject = res.Skill
	}
	banner := fmt.Sprintf("⚠ %s %s: scan found %d finding(s) (max %s; block threshold %s)",
		action, subject, res.Summary.Total(), res.Summary.MaxSeverity(), threshold)
	if blocked {
		banner = fmt.Sprintf("✗ %s %s: scan blocked (max %s ≥ threshold %s)",
			action, subject, res.Summary.MaxSeverity(), threshold)
	}
	fmt.Fprintln(printer.Err, banner)
	// Render only findings at or above the threshold when blocking; otherwise
	// show everything so the user has a complete picture of what was flagged.
	display := res.Findings
	if blocked {
		display = security.Filter(res.Findings, threshold)
	}
	for _, f := range display {
		loc := f.File
		if f.Line > 0 {
			loc = loc + ":" + strconv.Itoa(f.Line)
		}
		fmt.Fprintf(printer.Err, "  [%s] %s — %s",
			strings.ToUpper(string(f.Severity)), f.Check, f.Message)
		if loc != "" {
			fmt.Fprintf(printer.Err, " (%s)", loc)
		}
		fmt.Fprintln(printer.Err)
		if f.Remediation != "" {
			fmt.Fprintf(printer.Err, "    → %s\n", f.Remediation)
		}
	}
	if blocked {
		fmt.Fprintln(printer.Err, "  Pass --no-scan to override, or `qvr config set security.block_severity <higher>` to relax the gate.")
	}
}

// blockedScanError is the typed error returned when a gate blocks an
// operation. Callers (cmd-level) can wrap it with rollback information.
type blockedScanError struct {
	Subject   string
	Threshold security.Severity
	Result    *security.ScanResult
}

func (e *blockedScanError) Error() string {
	if e == nil {
		return "scan blocked"
	}
	max := security.Severity("")
	if e.Result != nil {
		max = e.Result.Summary.MaxSeverity()
	}
	return fmt.Sprintf("scan blocked %s (max severity %s ≥ threshold %s)",
		e.Subject, max, e.Threshold)
}

// gateAvailable reports whether the gate would do anything for this
// configuration. Useful when callers want to short-circuit expensive
// preparation work (e.g. registry add's per-skill temp worktree
// materialization).
func gateAvailable(cfg *config.Config, disabled bool) bool {
	if disabled {
		return false
	}
	if cfg == nil {
		return false
	}
	return cfg.Security.ScanOnInstall
}

// ensure the output import is retained even if all renderers use printer.Err
// directly.
var _ output.Format = output.FormatText
