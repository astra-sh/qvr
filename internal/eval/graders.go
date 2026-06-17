package eval

import (
	"fmt"
	"strings"
)

// GraderResult is one grader's verdict on one case.
type GraderResult struct {
	Type   string `json:"type"`
	Name   string `json:"name,omitempty"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// graderFunc judges evidence against a spec. Every grader is pure: same
// (spec, evidence) → same result, so an eval run is reproducible.
type graderFunc func(GraderSpec, *Evidence) GraderResult

// graders is the registry of deterministic, trace-based graders. Each reads the
// spans/meta qvr already captured — none runs an agent or calls a model. New
// grader types register here; the LLM-judge ("prompt") grader is deliberately
// NOT here — semantic judgement is layered on top as a skill, keeping the core
// gate model-free.
var graders = map[string]graderFunc{
	"outcome":          gradeOutcome,
	"text":             gradeText,
	"tool_sequence":    gradeToolSequence,
	"tool_constraint":  gradeToolConstraint,
	"skill_invocation": gradeSkillInvocation,
	"behavior":         gradeBehavior,
}

// runGrader dispatches one grader spec. An unknown type fails closed (a gate
// that silently skips an unrecognized grader is worse than no gate); Load
// already rejects these, so this is the belt-and-braces path.
func runGrader(g GraderSpec, e *Evidence) GraderResult {
	fn, ok := graders[g.Type]
	if !ok {
		return GraderResult{Type: g.Type, Name: g.Name, Pass: false, Detail: "unknown grader type"}
	}
	return fn(g, e)
}

// graderValidators rejects a grader whose config would make it a no-op — a gate
// that can never fail is worse than no gate, because it reads as evidence while
// asserting nothing. Each grader must carry at least one real assertion. Kept as
// a registry (parallel to `graders`) so each check stays tiny.
var graderValidators = map[string]func(GraderSpec) error{
	"outcome": func(g GraderSpec) error {
		return need(g.Expect != "", "needs `expect`")
	},
	"text": func(g GraderSpec) error {
		switch {
		case len(g.Contains) == 0 && len(g.Reject) == 0:
			return fmt.Errorf("needs `contains` or `reject`")
		case g.On != "" && g.On != "final_message":
			return fmt.Errorf("unknown `on` field %q (want final_message)", g.On)
		}
		return nil
	},
	"tool_sequence": func(g GraderSpec) error {
		return need(len(g.Sequence) > 0, "needs `sequence`")
	},
	"tool_constraint": func(g GraderSpec) error {
		return need(len(g.ExpectTools) > 0 || len(g.RejectTools) > 0 || g.MaxTools > 0,
			"needs `expectTools`, `rejectTools`, or `maxTools`")
	},
	"skill_invocation": func(g GraderSpec) error {
		return need(len(g.ExpectSkills) > 0 || len(g.RejectSkills) > 0, "needs `expectSkills` or `rejectSkills`")
	},
	"behavior": func(g GraderSpec) error {
		return need(g.MaxTools > 0 || g.MaxTurns > 0 || g.MaxDurationMs > 0,
			"needs `maxTools`, `maxTurns`, or `maxDurationMs`")
	},
}

// validateGraderConfig runs the registered config check for a grader type.
func validateGraderConfig(g GraderSpec) error {
	if v, ok := graderValidators[g.Type]; ok {
		return v(g)
	}
	return nil
}

// need returns an error with msg unless ok.
func need(ok bool, msg string) error {
	if ok {
		return nil
	}
	return fmt.Errorf("%s", msg)
}

func gradeOutcome(g GraderSpec, e *Evidence) GraderResult {
	pass := e.Outcome == g.Expect
	detail := ""
	if !pass {
		got := e.Outcome
		if got == "" {
			got = "unknown"
		}
		detail = fmt.Sprintf("outcome=%s, want %s", got, g.Expect)
	}
	return GraderResult{Type: g.Type, Name: g.Name, Pass: pass, Detail: detail}
}

func gradeText(g GraderSpec, e *Evidence) GraderResult {
	hay := evidenceField(g.On, e)
	for _, want := range g.Contains {
		if !strings.Contains(hay, want) {
			return fail(g, fmt.Sprintf("missing %q", want))
		}
	}
	for _, bad := range g.Reject {
		if strings.Contains(hay, bad) {
			return fail(g, fmt.Sprintf("contains rejected %q", bad))
		}
	}
	return pass(g)
}

// evidenceField selects the text body a text grader reads. final_message is the
// default and only field today. An unknown name returns "" (defensive: the
// manifest validator already rejects unknown `on` values, so a typo fails at
// load time rather than silently grading the wrong field here). Add new fields
// to this switch AND to the text validator together.
func evidenceField(name string, e *Evidence) string {
	switch name {
	case "", "final_message":
		return e.FinalMessage
	default:
		return ""
	}
}

func gradeToolSequence(g GraderSpec, e *Evidence) GraderResult {
	if isSubsequence(g.Sequence, e.ToolSequence) {
		return pass(g)
	}
	return fail(g, fmt.Sprintf("sequence %v not found in %v", g.Sequence, e.ToolSequence))
}

func gradeToolConstraint(g GraderSpec, e *Evidence) GraderResult {
	used := toSet(e.ToolSequence)
	for _, want := range g.ExpectTools {
		if !used[want] {
			return fail(g, fmt.Sprintf("expected tool %q never used", want))
		}
	}
	for _, bad := range g.RejectTools {
		if used[bad] {
			return fail(g, fmt.Sprintf("rejected tool %q was used", bad))
		}
	}
	if g.MaxTools > 0 && len(e.ToolSequence) > g.MaxTools {
		return fail(g, fmt.Sprintf("%d tool calls exceed max %d", len(e.ToolSequence), g.MaxTools))
	}
	return pass(g)
}

func gradeSkillInvocation(g GraderSpec, e *Evidence) GraderResult {
	fired := toSet(e.Skills)
	for _, want := range g.ExpectSkills {
		if !fired[want] {
			return fail(g, fmt.Sprintf("expected skill %q never fired", want))
		}
	}
	for _, bad := range g.RejectSkills {
		if fired[bad] {
			return fail(g, fmt.Sprintf("rejected skill %q fired", bad))
		}
	}
	return pass(g)
}

func gradeBehavior(g GraderSpec, e *Evidence) GraderResult {
	if g.MaxTools > 0 && e.Tools > g.MaxTools {
		return fail(g, fmt.Sprintf("%d tools exceed max %d", e.Tools, g.MaxTools))
	}
	if g.MaxTurns > 0 && e.Turns > g.MaxTurns {
		return fail(g, fmt.Sprintf("%d turns exceed max %d", e.Turns, g.MaxTurns))
	}
	if g.MaxDurationMs > 0 && e.DurationMs > g.MaxDurationMs {
		return fail(g, fmt.Sprintf("%dms exceeds max %dms", e.DurationMs, g.MaxDurationMs))
	}
	return pass(g)
}

// --- small helpers ---

func pass(g GraderSpec) GraderResult { return GraderResult{Type: g.Type, Name: g.Name, Pass: true} }
func fail(g GraderSpec, detail string) GraderResult {
	return GraderResult{Type: g.Type, Name: g.Name, Pass: false, Detail: detail}
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, it := range items {
		s[it] = true
	}
	return s
}

// isSubsequence reports whether want appears in order (not necessarily
// contiguously) within have. An empty want trivially matches.
func isSubsequence(want, have []string) bool {
	i := 0
	for _, h := range have {
		if i < len(want) && h == want[i] {
			i++
		}
	}
	return i == len(want)
}
