// Package output renders command results: a Printer that emits text tables
// or JSON per the global --output flag (structured data to stdout,
// diagnostics to stderr), TTY-aware styling, and progress indicators.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"unicode/utf8"
)

// Format represents the output format.
type Format string

// The supported output formats, matching the global --output flag values.
const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Printer handles formatted output.
type Printer struct {
	Out    io.Writer
	Err    io.Writer
	Format Format
	// Verbose opts into play-by-play output: per-item progress (Step), follow-up
	// Hints, and de-emphasised Detail lines. Off by default so every command's
	// default surface is the essential outcome plus anything actionable (errors,
	// warnings, the final verdict). Set from the global -v/--verbose flag.
	Verbose bool
}

// New creates a new Printer with the given format.
func New(format Format) *Printer {
	return &Printer{
		Out:    os.Stdout,
		Err:    os.Stderr,
		Format: format,
	}
}

// StyleOut returns the Styler for the Printer's stdout stream. Use it to
// emphasise fragments inside Success/Info lines (names bold, secondary
// detail dim). Plain pass-through when stdout is not a terminal.
func (p *Printer) StyleOut() Styler { return NewStyler(p.Out) }

// StyleErr returns the Styler for the Printer's stderr stream.
func (p *Printer) StyleErr() Styler { return NewStyler(p.Err) }

// Success prints a success message (text mode only).
func (p *Printer) Success(msg string) {
	if p.Format == FormatText {
		fmt.Fprintf(p.Out, "%s %s\n", p.StyleOut().Green("✓"), msg)
	}
}

// Error prints an error message with a uv-style `error:` prefix (text mode
// only).
func (p *Printer) Error(msg string) {
	if p.Format == FormatText {
		fmt.Fprintf(p.Err, "%s %s\n", p.StyleErr().BoldRed("error:"), msg)
	}
}

// Warning prints a warning message with a uv-style `warning:` prefix (text
// mode only).
func (p *Printer) Warning(msg string) {
	if p.Format == FormatText {
		fmt.Fprintf(p.Err, "%s %s\n", p.StyleErr().BoldYellow("warning:"), msg)
	}
}

// Hint prints a follow-up suggestion with a uv-style `hint:` prefix. Hints
// go to stderr — they are guidance about the run, not command output, so
// piped stdout stays clean. Suppressed unless --verbose: a hint is optional
// next-step guidance, never the outcome, so it stays out of the default
// minimal surface (text mode only).
func (p *Printer) Hint(msg string) {
	if p.Format == FormatText && p.Verbose {
		fmt.Fprintf(p.Err, "%s %s\n", p.StyleErr().BoldCyan("hint:"), msg)
	}
}

// Info prints an info message (text mode only). Info is a command's PRIMARY
// output (list rows, status, the queried result), so it is NOT gated by
// verbosity — gate play-by-play progress with Step, not Info.
func (p *Printer) Info(msg string) {
	if p.Format == FormatText {
		fmt.Fprintln(p.Out, msg)
	}
}

// Step prints a single line of play-by-play progress (e.g. "Restored X",
// "Linked Y", "Registered Z") — the per-item narration of a multi-step
// command. Suppressed unless --verbose so the default surface is the final
// verdict, not every intermediate step (text mode only).
func (p *Printer) Step(msg string) {
	if p.Format == FormatText && p.Verbose {
		fmt.Fprintln(p.Out, msg)
	}
}

// Detail prints an indented, de-emphasised follow-up line under a preceding
// Success/Info line. Suppressed unless --verbose — it is secondary elaboration,
// not the outcome (text mode only).
func (p *Printer) Detail(msg string) {
	if p.Format == FormatText && p.Verbose {
		fmt.Fprintf(p.Out, "  %s\n", p.StyleOut().Dim(msg))
	}
}

// JSON prints data as JSON. Works in both modes.
func (p *Printer) JSON(data any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// ansiEscapeRe matches ANSI CSI sequences (colors, cursor movement), OSC
// sequences (terminal title, hyperlinks; BEL- or ST-terminated), and any
// remaining bare ESC-prefixed control. Skill descriptions are attacker-
// controlled registry content, so anything that could drive the terminal is
// stripped before rendering.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[@-_]`)

// SanitizeDesc neutralises a description for terminal output: ANSI/OSC escape
// sequences are stripped, newlines/tabs flatten to single spaces, and all
// other control characters are dropped. Registry descriptions are untrusted
// input — a hostile skill's description must not be able to clear the screen,
// retitle the terminal, or smuggle multi-line output into a table row.
func SanitizeDesc(s string) string {
	s = ansiEscapeRe.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// TruncDesc renders a description for a text-mode table cell. The string is
// sanitised first (see SanitizeDesc); in `full` mode it then passes through
// unclipped, otherwise it clips to 60 chars with an ellipsis. Centralised here
// so search, list, and registry list all agree on the shape of truncation.
func TruncDesc(s string, full bool) string {
	s = SanitizeDesc(s)
	if full || utf8.RuneCountInString(s) <= 60 {
		return s
	}
	runes := []rune(s)
	return string(runes[:57]) + "..."
}

// Table prints a table with headers and rows.
func (p *Printer) Table(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(p.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}
