package eval

// CaseResult is the verdict on one case: it passes only when every grader
// passed. The per-grader results are retained for diagnostics.
type CaseResult struct {
	Case    string         `json:"case"`
	Pass    bool           `json:"pass"`
	Graders []GraderResult `json:"graders"`
}

// SuiteResult aggregates a suite's case verdicts.
type SuiteResult struct {
	Suite  string       `json:"suite"`
	Pass   bool         `json:"pass"`
	Passed int          `json:"passed"`
	Failed int          `json:"failed"`
	Cases  []CaseResult `json:"cases"`
}

// GradeCase runs every grader in a case against the evidence. The case passes
// only when all of them pass (AND semantics — a gate is as strong as its
// strictest check).
func GradeCase(c Case, e *Evidence) CaseResult {
	r := CaseResult{Case: c.Name, Pass: true}
	for _, g := range c.Graders {
		gr := runGrader(g, e)
		r.Graders = append(r.Graders, gr)
		if !gr.Pass {
			r.Pass = false
		}
	}
	return r
}

// GradeSuite grades every case in a suite against the same evidence and rolls
// up the pass/fail counts. The suite passes only when every case passes.
func GradeSuite(s *Suite, e *Evidence) SuiteResult {
	res := SuiteResult{Suite: s.Name, Pass: true}
	for _, c := range s.Cases {
		cr := GradeCase(c, e)
		res.Cases = append(res.Cases, cr)
		if cr.Pass {
			res.Passed++
		} else {
			res.Failed++
			res.Pass = false
		}
	}
	return res
}
