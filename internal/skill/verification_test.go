package skill_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
)

// makeSkillRepo creates a non-bare git repo with a skill at "skills/<name>".
// Returns the repo path and the canonical subpath.
func makeSkillRepo(t *testing.T, name, content string, extra map[string]string) string {
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
	skillRel := filepath.Join("skills", name)
	skillAbs := filepath.Join(dir, skillRel)
	if err := os.MkdirAll(skillAbs, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillAbs, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add(filepath.Join(skillRel, "SKILL.md")); err != nil {
		t.Fatalf("add: %v", err)
	}
	for rel, body := range extra {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir extra: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write extra: %v", err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("add extra: %v", err)
		}
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

func TestPopulateVerification_unverifiedByDefault(t *testing.T) {
	repo := makeSkillRepo(t, "foo", "---\nname: foo\n---\nbody\n", nil)
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	rec := skill.PopulateVerification(entry, model.ProvenanceRef{
		RegistryName: "r",
		RegistryURL:  "https://example.invalid/r.git",
		Ref:          "main",
		Subpath:      "skills/foo",
	})
	if rec == nil {
		t.Fatal("PopulateVerification returned nil for git-source entry")
	}
	if rec.Status != model.StatusUnverified {
		t.Errorf("Status = %q, want %q", rec.Status, model.StatusUnverified)
	}
	if rec.SubtreeHash == "" {
		t.Errorf("SubtreeHash empty")
	}
	if rec.CommitSHA == "" || rec.TreeSHA == "" {
		t.Errorf("CommitSHA/TreeSHA empty")
	}
	if rec.Provenance.RegistryURL != "https://example.invalid/r.git" {
		t.Errorf("Provenance not carried through")
	}
	if len(rec.Warnings) == 0 || rec.Warnings[0] == "" {
		t.Errorf("expected an unsigned warning")
	}
}

func TestPopulateVerification_linkEntryReturnsNil(t *testing.T) {
	entry := &model.LockEntry{
		Name:       "linked",
		LinkTarget: "/some/path",
		Source:     "link",
	}
	if rec := skill.PopulateVerification(entry, model.ProvenanceRef{}); rec != nil {
		t.Errorf("link entry should return nil VerificationRecord, got %+v", rec)
	}
}

func TestPopulateVerification_signatureFlagsUntrusted(t *testing.T) {
	// A skill carrying a qvr.sig envelope should be flagged "untrusted" —
	// signature found, no verifier wired yet. Phase 5 promotes this to
	// "verified" / "untrusted" / "failed" based on real validation.
	sig := `{
  "version": "qvr-signature-v1",
  "algorithm": "ed25519",
  "hash": "sha256",
  "artifact_type": "qvr.skill.directory",
  "signed_at": "2026-05-27T12:00:00Z",
  "manifest_digest": "sha256:abc",
  "public_key": "pk",
  "signature": "sig"
}`
	repo := makeSkillRepo(t, "foo", "---\nname: foo\n---\nbody\n", map[string]string{
		"skills/foo/qvr.sig": sig,
	})
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	rec := skill.PopulateVerification(entry, model.ProvenanceRef{})
	if rec == nil {
		t.Fatal("nil record")
	}
	if rec.Status != model.StatusUntrusted {
		t.Errorf("Status = %q, want %q", rec.Status, model.StatusUntrusted)
	}
	if rec.Signature == nil {
		t.Fatal("Signature block not populated")
	}
	if rec.Signature.Algorithm != "ed25519" {
		t.Errorf("Signature.Algorithm = %q, want ed25519", rec.Signature.Algorithm)
	}
	if rec.Signature.ManifestDigest != rec.SubtreeHash {
		t.Errorf("Signature.ManifestDigest should equal SubtreeHash")
	}
}

func TestRefreshVerification_preservesProvenanceButRecomputesHash(t *testing.T) {
	repo := makeSkillRepo(t, "foo", "---\nname: foo\n---\nv1\n", nil)
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	entry.Verification = skill.PopulateVerification(entry, model.ProvenanceRef{
		RegistryName: "test-reg",
		RegistryURL:  "https://example.invalid/repo.git",
		Ref:          "v1.0.0",
		Subpath:      "skills/foo",
	})
	originalHash := entry.Verification.SubtreeHash
	originalURL := entry.Verification.Provenance.RegistryURL

	// Mutate the worktree on a new commit, simulating a `qvr pull` that
	// fast-forwarded HEAD. The refresh should record the new hash while
	// keeping the original Provenance fields intact.
	mutPath := filepath.Join(repo, "skills/foo/SKILL.md")
	if err := os.WriteFile(mutPath, []byte("---\nname: foo\n---\nv2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, _ := gogit.PlainOpen(repo)
	wt, _ := r.Worktree()
	if _, err := wt.Add("skills/foo/SKILL.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("v2", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(1, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	skill.RefreshVerification(entry)
	if entry.Verification.SubtreeHash == originalHash {
		t.Errorf("hash did not refresh after content change")
	}
	if entry.Verification.Provenance.RegistryURL != originalURL {
		t.Errorf("Provenance lost on refresh")
	}
}
