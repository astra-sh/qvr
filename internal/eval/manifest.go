// Package eval is qvr's deterministic, trace-based skill-evaluation substrate:
// it reads an evals.yaml manifest (a sibling of SKILL.md, qvr's own format — not
// part of the agentskills.io spec) and grades a skill's CAPTURED sessions
// against it. The grading is pure Go over the spans qvr already derived from
// real usage — no model calls, no agent execution, no sandbox replay. That keeps
// the gate inside qvr's "uv for skills" core; semantic LLM-judge grading and
// running a candidate skill against tasks are layered on top as skills, not
// built into this binary.
package eval

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ManifestFile is the conventional eval manifest name, a sibling of SKILL.md.
const ManifestFile = "evals.yaml"

// Manifest is a skill's eval definition: named suites of named cases, each a set
// of graders that must all pass for the case to pass.
type Manifest struct {
	Version int     `yaml:"version" json:"version"`
	Suites  []Suite `yaml:"suites" json:"suites"`
}

// Suite is a named group of cases (e.g. "triage-correctness", "safety").
type Suite struct {
	Name  string `yaml:"name" json:"name"`
	Cases []Case `yaml:"cases" json:"cases"`
}

// Case is one named expectation, satisfied only when every grader passes.
type Case struct {
	Name    string       `yaml:"name" json:"name"`
	Graders []GraderSpec `yaml:"graders" json:"graders"`
}

// GraderSpec configures one grader. It is intentionally a flat struct: each
// grader type reads only the fields it cares about, which keeps YAML authoring
// simple and avoids polymorphic decoding. Unknown grader types fail loudly at
// run time rather than being silently skipped (a silently-skipped gate is worse
// than no gate).
type GraderSpec struct {
	Type string `yaml:"type" json:"type"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// outcome grader: the session-level qvr.outcome must equal Expect.
	Expect string `yaml:"expect,omitempty" json:"expect,omitempty"`

	// text grader: every Contains substring must appear and no Reject substring
	// may appear in the field named by On (default: final_message).
	On       string   `yaml:"on,omitempty" json:"on,omitempty"`
	Contains []string `yaml:"contains,omitempty" json:"contains,omitempty"`
	Reject   []string `yaml:"reject,omitempty" json:"reject,omitempty"`

	// tool_sequence grader: Sequence must appear as an ordered subsequence of
	// the session's tool calls.
	Sequence []string `yaml:"sequence,omitempty" json:"sequence,omitempty"`

	// tool_constraint grader: every ExpectTools tool must have been used, no
	// RejectTools tool may have been used, and (when >0) the call count must not
	// exceed MaxTools.
	ExpectTools []string `yaml:"expectTools,omitempty" json:"expect_tools,omitempty"`
	RejectTools []string `yaml:"rejectTools,omitempty" json:"reject_tools,omitempty"`

	// skill_invocation grader: ExpectSkills must all have fired, RejectSkills
	// none.
	ExpectSkills []string `yaml:"expectSkills,omitempty" json:"expect_skills,omitempty"`
	RejectSkills []string `yaml:"rejectSkills,omitempty" json:"reject_skills,omitempty"`

	// behavior grader: efficiency ceilings (0 = ignored).
	MaxTools      int   `yaml:"maxTools,omitempty" json:"max_tools,omitempty"`
	MaxTurns      int   `yaml:"maxTurns,omitempty" json:"max_turns,omitempty"`
	MaxDurationMs int64 `yaml:"maxDurationMs,omitempty" json:"max_duration_ms,omitempty"`
}

// Load reads and validates the evals.yaml in skillDir. Returns (nil, nil) when
// the skill has no manifest — an un-evaluated skill is not an error.
func Load(skillDir string) (*Manifest, error) {
	path := filepath.Join(skillDir, ManifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes and validates a manifest from raw YAML bytes.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse evals.yaml: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// validate enforces the structural invariants: a supported version, named
// non-empty suites and cases, and only known grader types.
func (m *Manifest) validate() error {
	if m.Version != 1 {
		return fmt.Errorf("evals.yaml: unsupported version %d (want 1)", m.Version)
	}
	if len(m.Suites) == 0 {
		return fmt.Errorf("evals.yaml: no suites defined")
	}
	for _, s := range m.Suites {
		if s.Name == "" {
			return fmt.Errorf("evals.yaml: a suite has no name")
		}
		if len(s.Cases) == 0 {
			return fmt.Errorf("evals.yaml: suite %q has no cases", s.Name)
		}
		for _, c := range s.Cases {
			if err := validateCase(s.Name, c); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCase(suite string, c Case) error {
	if c.Name == "" {
		return fmt.Errorf("evals.yaml: a case in suite %q has no name", suite)
	}
	if len(c.Graders) == 0 {
		return fmt.Errorf("evals.yaml: case %q/%q has no graders", suite, c.Name)
	}
	for _, g := range c.Graders {
		if _, ok := graders[g.Type]; !ok {
			return fmt.Errorf("evals.yaml: case %q/%q uses unknown grader type %q", suite, c.Name, g.Type)
		}
		if err := validateGraderConfig(g); err != nil {
			return fmt.Errorf("evals.yaml: case %q/%q %s grader %w", suite, c.Name, g.Type, err)
		}
	}
	return nil
}

// Suite returns the named suite, or nil when absent.
func (m *Manifest) Suite(name string) *Suite {
	for i := range m.Suites {
		if m.Suites[i].Name == name {
			return &m.Suites[i]
		}
	}
	return nil
}
