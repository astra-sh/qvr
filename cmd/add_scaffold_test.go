package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
)

// stderrString returns what the test printer captured on its error stream.
func stderrString(t *testing.T) string {
	t.Helper()
	buf, ok := printer.Err.(interface{ String() string })
	if !ok {
		t.Fatalf("printer.Err is not a stringer; got %T", printer.Err)
	}
	return buf.String()
}

// TestRunAdd_FailedAdd_NoScaffold is the #245 guard: a fully-failed add (e.g.
// a typo'd skill name) in a pristine directory must not leave qvr.toml or
// qvr.lock behind, must not print the "created qvr.toml" hint, and must not
// register the directory in projects.json (which would warn "project lock
// vanished" forever after the dir is deleted).
func TestRunAdd_FailedAdd_NoScaffold(t *testing.T) {
	project, _ := setupProjectFileTest(t)

	err := runAdd(addCmd, []string{"no-such-skill"})
	if err == nil {
		t.Fatal("add of an unknown skill returned nil; want non-zero exit")
	}
	if _, serr := os.Stat(filepath.Join(project, "qvr.lock")); !os.IsNotExist(serr) {
		t.Errorf("failed add scaffolded qvr.lock (stat err: %v) (#245)", serr)
	}
	if _, serr := os.Stat(model.DefaultProjectPath(project)); !os.IsNotExist(serr) {
		t.Errorf("failed add scaffolded qvr.toml (#245)")
	}
	if got := stderrString(t); strings.Contains(got, "created qvr.toml") {
		t.Errorf("failed add printed the success hint: %q (#245)", got)
	}
	pf, perr := registry.ReadProjects()
	if perr != nil {
		t.Fatalf("read projects: %v", perr)
	}
	lockAbs, _ := filepath.Abs(filepath.Join(project, "qvr.lock"))
	if _, ok := pf.Projects[lockAbs]; ok {
		t.Errorf("failed add registered %s in projects.json (#245)", lockAbs)
	}
}

// TestRunAdd_FailedAdd_PreexistingLockUntouched: when a lock already exists,
// a failed add keeps it (and its entries) — only the pristine-dir scaffold is
// suppressed.
func TestRunAdd_FailedAdd_PreexistingLockUntouched(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("seed add: %v", err)
	}

	if err := runAdd(addCmd, []string{"no-such-skill"}); err == nil {
		t.Fatal("add of an unknown skill returned nil; want non-zero exit")
	}
	lock, lerr := model.ReadLockFile(filepath.Join(project, "qvr.lock"))
	if lerr != nil {
		t.Fatalf("read lock after failed add: %v", lerr)
	}
	if _, gerr := lock.Get("code-review"); gerr != nil {
		t.Errorf("pre-existing entry lost after failed add: %v", gerr)
	}
}

// TestRunAdd_MixedBatch_ScaffoldsAndExits1: a batch where one skill resolves
// and another doesn't keeps the partial success (lock + toml written, hint
// printed) while still exiting non-zero.
func TestRunAdd_MixedBatch_ScaffoldsAndExits1(t *testing.T) {
	project, _ := setupProjectFileTest(t)

	err := runAdd(addCmd, []string{"code-review", "no-such-skill"})
	if err == nil {
		t.Fatal("mixed batch returned nil; want non-zero exit")
	}
	if _, serr := os.Stat(filepath.Join(project, "qvr.lock")); serr != nil {
		t.Errorf("partial success must write qvr.lock: %v", serr)
	}
	if _, serr := os.Stat(model.DefaultProjectPath(project)); serr != nil {
		t.Errorf("partial success must write qvr.toml: %v", serr)
	}
	if got := stderrString(t); !strings.Contains(got, "created qvr.toml") {
		t.Errorf("partial success should keep the qvr.toml hint; stderr = %q", got)
	}
}
