package eval

import (
	"context"
	"fmt"

	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// RunInput parameterizes one eval run. SessionID pins which captured session to
// grade; when nil, the skill's most recent session is used (the "online eval"
// default — grade the latest real run). Suite "" grades every suite.
type RunInput struct {
	SkillName string
	SkillDir  string
	Suite     string
	SessionID *uuid.UUID
}

// RunResult is the outcome of grading one session against the manifest.
type RunResult struct {
	Skill     string        `json:"skill"`
	SessionID string        `json:"session_id"`
	Pass      bool          `json:"pass"`
	Passed    int           `json:"passed"`
	Failed    int           `json:"failed"`
	Suites    []SuiteResult `json:"suites"`
}

// Run loads the skill's evals.yaml, grades a captured session against the
// requested suite(s), and returns the result. It NEVER runs an agent or calls a
// model — it grades spans qvr already captured. Writing the result to the
// eval-run store (the lock × evidence join) is the caller's job.
func Run(ctx context.Context, s store.Store, in RunInput) (*RunResult, error) {
	man, err := Load(in.SkillDir)
	if err != nil {
		return nil, err
	}
	if man == nil {
		return nil, fmt.Errorf("skill %q has no %s in %s", in.SkillName, ManifestFile, in.SkillDir)
	}

	suites, err := selectSuites(man, in.Suite)
	if err != nil {
		return nil, err
	}

	sess, err := resolveSession(ctx, s, in)
	if err != nil {
		return nil, err
	}
	spans, err := s.QuerySpans(ctx, &store.SpanFilter{SessionID: &sess.SessionID})
	if err != nil {
		return nil, fmt.Errorf("load spans for %s: %w", sess.SessionID, err)
	}
	ev := BuildEvidence(sess, spans)
	ev.Skill = in.SkillName

	res := &RunResult{Skill: in.SkillName, SessionID: ev.SessionID, Pass: true}
	for i := range suites {
		sr := GradeSuite(&suites[i], ev)
		res.Suites = append(res.Suites, sr)
		res.Passed += sr.Passed
		res.Failed += sr.Failed
		if !sr.Pass {
			res.Pass = false
		}
	}
	return res, nil
}

// selectSuites resolves the requested suite name to a slice — one named suite,
// or all of them when name is empty.
func selectSuites(man *Manifest, name string) ([]Suite, error) {
	if name == "" {
		return man.Suites, nil
	}
	su := man.Suite(name)
	if su == nil {
		return nil, fmt.Errorf("no suite %q in evals.yaml", name)
	}
	return []Suite{*su}, nil
}

// resolveSession finds the session to grade: the pinned id, or the skill's most
// recent captured session.
func resolveSession(ctx context.Context, s store.Store, in RunInput) (*store.SessionMetaRow, error) {
	if in.SessionID != nil {
		m, err := s.GetSessionMeta(ctx, *in.SessionID)
		if err != nil {
			return nil, fmt.Errorf("look up session %s: %w", in.SessionID, err)
		}
		if m == nil {
			return nil, fmt.Errorf("no captured session %s", in.SessionID)
		}
		return m, nil
	}
	rows, err := s.ListSessionMeta(ctx, &store.SessionMetaFilter{Skill: in.SkillName, Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("find latest session for %q: %w", in.SkillName, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no captured sessions for skill %q to evaluate — run it, then `qvr audit discover`", in.SkillName)
	}
	return rows[0], nil
}
