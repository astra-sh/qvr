package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
)

// TestProjectInit_EmptyDirScaffoldsTomlNoLock pins the uv-init parity: a bare
// `qvr init` writes a well-formed qvr.toml (banner + [project]) and NOTHING
// else — no qvr.lock, no skill dir, no default-targets line (greenfield).
func TestProjectInit_EmptyDirScaffoldsTomlNoLock(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)

	if err := runProjectInit(initCmd, nil); err != nil {
		t.Fatalf("runProjectInit: %v", err)
	}

	raw, err := os.ReadFile(model.DefaultProjectPath(project))
	if err != nil {
		t.Fatalf("qvr.toml not scaffolded: %v", err)
	}
	if !strings.Contains(string(raw), "Reserved for future milestones") {
		t.Errorf("scaffold missing reserved-section banner:\n%s", raw)
	}
	if strings.Contains(string(raw), "default-targets") {
		t.Errorf("greenfield init should omit default-targets, got:\n%s", raw)
	}

	proj := readProj(t, project)
	if proj.Project.Name != filepath.Base(project) {
		t.Errorf("name = %q, want %q", proj.Project.Name, filepath.Base(project))
	}
	if proj.Project.Version != "0.1.0" {
		t.Errorf("version = %q, want 0.1.0", proj.Project.Version)
	}
	// Laziness: no qvr.lock, like uv defers uv.lock.
	if _, err := os.Stat(filepath.Join(project, model.LockFileName)); !os.IsNotExist(err) {
		t.Errorf("qvr init must not create qvr.lock (stat err = %v)", err)
	}
	// No skill dirs created.
	if _, err := os.Stat(filepath.Join(project, ".claude")); !os.IsNotExist(err) {
		t.Errorf("qvr init must not scaffold a skill dir")
	}
}

// TestProjectInit_InfersExistingClaudeTarget: a uniquely-owned dir resolves to
// its sole owner.
func TestProjectInit_InfersExistingClaudeTarget(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)
	if err := os.MkdirAll(filepath.Join(project, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runProjectInit(initCmd, nil); err != nil {
		t.Fatalf("runProjectInit: %v", err)
	}
	got := readProj(t, project).Project.DefaultTargets
	if len(got) != 1 || got[0] != "claude" {
		t.Errorf("default-targets = %v, want [claude]", got)
	}
}

// TestProjectInit_SharedAgentsDirInfersUniversalProject is the load-bearing
// dedupe case: .agents/skills is owned by codex/cursor/gemini AND the universal
// "project" target, so inference must collapse to a single "project" entry.
func TestProjectInit_SharedAgentsDirInfersUniversalProject(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)
	if err := os.MkdirAll(filepath.Join(project, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runProjectInit(initCmd, nil); err != nil {
		t.Fatalf("runProjectInit: %v", err)
	}
	got := readProj(t, project).Project.DefaultTargets
	if len(got) != 1 || got[0] != "project" {
		t.Errorf("default-targets = %v, want [project] (no codex/cursor/gemini dupes)", got)
	}
}

// TestProjectInit_MultipleUniqueTargets: several uniquely-owned dirs combine,
// sorted.
func TestProjectInit_MultipleUniqueTargets(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)
	for _, d := range []string{".claude/skills", ".github/skills"} {
		if err := os.MkdirAll(filepath.Join(project, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := runProjectInit(initCmd, nil); err != nil {
		t.Fatalf("runProjectInit: %v", err)
	}
	got := readProj(t, project).Project.DefaultTargets
	if len(got) != 2 || got[0] != "claude" || got[1] != "copilot" {
		t.Errorf("default-targets = %v, want [claude copilot]", got)
	}
}

// TestProjectInit_ErrorsOnExistingToml: uv-on-existing-pyproject parity. The
// file must be untouched.
func TestProjectInit_ErrorsOnExistingToml(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)

	projPath := model.DefaultProjectPath(project)
	const sentinel = "# pre-existing, do not touch\n"
	if err := os.WriteFile(projPath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runProjectInit(initCmd, nil); err == nil {
		t.Fatal("expected error on existing qvr.toml, got nil")
	}
	after, _ := os.ReadFile(projPath)
	if string(after) != sentinel {
		t.Errorf("existing qvr.toml was clobbered: %q", after)
	}
}

// TestProjectInit_NameArgOverridesBasename: the optional [name] arg wins over
// the cwd basename.
func TestProjectInit_NameArgOverridesBasename(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)

	if err := runProjectInit(initCmd, []string{"my-proj"}); err != nil {
		t.Fatalf("runProjectInit: %v", err)
	}
	if got := readProj(t, project).Project.Name; got != "my-proj" {
		t.Errorf("name = %q, want my-proj", got)
	}
}

// TestProjectInit_JSONOutput asserts the documented JSON shape.
func TestProjectInit_JSONOutput(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	buf := withCapturingPrinter(t, output.FormatJSON)
	if err := os.MkdirAll(filepath.Join(project, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runProjectInit(initCmd, nil); err != nil {
		t.Fatalf("runProjectInit: %v", err)
	}

	var out struct {
		Name           string   `json:"name"`
		Path           string   `json:"path"`
		Version        string   `json:"version"`
		DefaultTargets []string `json:"default_targets"`
		Inferred       bool     `json:"inferred"`
		Created        string   `json:"created"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal JSON %q: %v", buf.String(), err)
	}
	if out.Version != "0.1.0" || out.Created != "qvr.toml" || !out.Inferred {
		t.Errorf("unexpected JSON: %+v", out)
	}
	if len(out.DefaultTargets) != 1 || out.DefaultTargets[0] != "claude" {
		t.Errorf("default_targets = %v, want [claude]", out.DefaultTargets)
	}
}
