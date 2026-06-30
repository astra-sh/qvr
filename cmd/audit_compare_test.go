package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
)

// TestCompareVersionLabel pins the version-label parity fix. compare now TRUSTS
// the version FROZEN on each cohort's spans — enrichment proves it against the
// run-immutable body and freezes it, so the label is the version that actually
// ran, never the live checkout (the old corroborate-against-current-lock
// behaviour that left every non-current cohort blank). It renders gracefully:
// the bare ref when proven (monorepo "<skill>/" prefix trimmed), the short
// commit when only that is proven, and "unknown" when neither — so an old or
// unprovable cohort reads honestly instead of borrowing another version's label.
// This mirrors the exact expression renderCompare uses for the REF column.
func TestCompareVersionLabel(t *testing.T) {
	const skill = "slugify-title"
	for _, tc := range []struct {
		name, ref, commit, want string
	}{
		{"frozen ref, monorepo prefix trimmed", "slugify-title/v0.5.0", "2c20399", "v0.5.0"},
		{"no ref falls back to short commit", "", "c8a69fb00000000", "c8a69fb"},
		{"neither proven reads unknown", "", "", derive.UnknownVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := derive.SkillVersionLabel(strings.TrimPrefix(tc.ref, skill+"/"), tc.commit)
			if got != tc.want {
				t.Errorf("label(ref=%q commit=%q) = %q, want %q", tc.ref, tc.commit, got, tc.want)
			}
		})
	}
}

// TestWarnMetricMismatch pins compare's silent-blank guard: when the requested
// --metric carries no grades but the skill has grades under another metric, the
// hint names it; when the requested metric DOES have grades, or output is JSON,
// it stays silent (no spurious nudge, no pollution of machine output).
func TestWarnMetricMismatch(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.OpenOptions{Path: filepath.Join(t.TempDir(), "s.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// slugify is graded under "exact" only — never under the default "score".
	if err := st.PutScore(ctx, "claude", "ref-1", "slugify", "exact", 1.0, "exact"); err != nil {
		t.Fatalf("put score: %v", err)
	}
	ungraded := []*store.SkillContentCohort{{ContentHash: "sha256:x"}} // MeanScore nil

	for _, tc := range []struct {
		name, metric, format string
		wantHint             bool
	}{
		{"mismatch nudges to the graded metric", "score", "text", true},
		{"requested metric has grades — silent", "exact", "text", false},
		{"json output is never polluted", "score", "json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			prevP, prevFmt, prevMetric := printer, outputFormat, compareMetric
			printer = &output.Printer{Out: out, Err: out, Format: output.FormatText}
			outputFormat, compareMetric = tc.format, tc.metric
			t.Cleanup(func() { printer, outputFormat, compareMetric = prevP, prevFmt, prevMetric })

			warnMetricMismatch(ctx, st, "slugify", ungraded)

			hinted := strings.Contains(out.String(), `--metric exact`)
			if hinted != tc.wantHint {
				t.Errorf("hint=%v, want %v (output: %q)", hinted, tc.wantHint, out.String())
			}
		})
	}
}

func scorePtr(v float64) *float64 { return &v }
func tokPtr(v int64) *int64       { return &v }

// TestMarkPareto checks the per-agent (quality, cost) frontier marking: a
// dominated cell is unmarked, mutually non-dominating cells are both on the
// frontier, and a cell missing either axis is incomparable (never marked, never
// dominates).
func TestMarkPareto(t *testing.T) {
	// claude: A beats B on both axes (higher score, fewer tokens) → only A.
	a := &store.SkillContentCohort{Agent: "claude", MeanScore: scorePtr(0.9), InputTokens: tokPtr(100)}
	b := &store.SkillContentCohort{Agent: "claude", MeanScore: scorePtr(0.8), InputTokens: tokPtr(150)}
	// codex: C cheaper/worse, D better/pricier → genuine tradeoff, both on frontier.
	c := &store.SkillContentCohort{Agent: "codex", MeanScore: scorePtr(0.7), InputTokens: tokPtr(50)}
	d := &store.SkillContentCohort{Agent: "codex", MeanScore: scorePtr(0.9), InputTokens: tokPtr(200)}
	// codex but no score → incomparable; must not be marked nor knock out C/D.
	e := &store.SkillContentCohort{Agent: "codex", InputTokens: tokPtr(10)}

	markPareto([]*store.SkillContentCohort{a, b, c, d, e})

	for _, tc := range []struct {
		name string
		c    *store.SkillContentCohort
		want bool
	}{
		{"A dominates B", a, true},
		{"B dominated", b, false},
		{"C tradeoff", c, true},
		{"D tradeoff", d, true},
		{"E incomparable", e, false},
	} {
		if tc.c.Pareto != tc.want {
			t.Errorf("%s: Pareto=%v, want %v", tc.name, tc.c.Pareto, tc.want)
		}
	}
}

// TestMarkPareto_DoesNotCrossAgents: a claude cell never dominates a codex cell;
// each agent's sole comparable cell is its own frontier.
func TestMarkPareto_DoesNotCrossAgents(t *testing.T) {
	cl := &store.SkillContentCohort{Agent: "claude", MeanScore: scorePtr(0.9), InputTokens: tokPtr(100)}
	co := &store.SkillContentCohort{Agent: "codex", MeanScore: scorePtr(0.5), InputTokens: tokPtr(300)}
	markPareto([]*store.SkillContentCohort{cl, co})
	if !cl.Pareto || !co.Pareto {
		t.Errorf("each agent's only cell is its own frontier: claude=%v codex=%v", cl.Pareto, co.Pareto)
	}
}
