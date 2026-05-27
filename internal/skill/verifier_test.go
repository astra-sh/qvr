package skill_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
)

// makeVerifierTestRepo seeds a non-bare git repo at t.TempDir() containing
// a skill at skills/foo/SKILL.md and returns the repo path.
func makeVerifierTestRepo(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	skillDir := filepath.Join(dir, "skills/foo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("skills/foo/SKILL.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

func TestVerifySingleEntry_ok(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\nbody\n")
	id, err := canonical.HashSubtree(repo, "skills/foo")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
		Verification: &model.VerificationRecord{
			SubtreeHash: id.SubtreeHash,
			TreeSHA:     id.TreeSHA,
			CommitSHA:   id.CommitSHA,
			Status:      model.StatusUnverified,
		},
	}
	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusOK {
		t.Errorf("Status = %q, want %q (drift=%+v message=%q)", got.Status, skill.VerifyStatusOK, got.Drift, got.Message)
	}
	if len(got.Drift) != 0 {
		t.Errorf("Drift should be empty, got %+v", got.Drift)
	}
}

func TestVerifySingleEntry_driftOnTamper(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\noriginal\n")
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	entry.Verification = skill.PopulateVerification(entry, model.ProvenanceRef{})

	// Tamper: commit a new version on top, simulating an upstream change
	// that the lockfile hasn't been refreshed against.
	if err := os.WriteFile(
		filepath.Join(repo, "skills/foo/SKILL.md"),
		[]byte("---\nname: foo\n---\nMUTATED\n"),
		0o644,
	); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, _ := gogit.PlainOpen(repo)
	wt, _ := r.Worktree()
	if _, err := wt.Add("skills/foo/SKILL.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("tamper", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(1, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusDrift {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusDrift)
	}
	foundSubtreeDrift := false
	for _, d := range got.Drift {
		if d.Field == "subtreeHash" {
			foundSubtreeDrift = true
		}
	}
	if !foundSubtreeDrift {
		t.Errorf("expected subtreeHash in drift list, got %+v", got.Drift)
	}
}

func TestVerifySingleEntry_unverifiedWhenNoBlock(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\nbody\n")
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
		// No Verification — represents a v2-loaded entry.
	}
	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusUnverified {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusUnverified)
	}
}

func TestVerifySingleEntry_missingWorktree(t *testing.T) {
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: filepath.Join(t.TempDir(), "does-not-exist"),
		Path:     "skills/foo",
		Source:   "registry",
	}
	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusMissing {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusMissing)
	}
}

func TestVerifySingleEntry_linkSkipped(t *testing.T) {
	entry := &model.LockEntry{
		Name:       "foo",
		LinkTarget: "/some/local/path",
		Source:     "link",
	}
	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusLink {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusLink)
	}
}
