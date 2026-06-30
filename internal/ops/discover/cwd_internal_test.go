package discover

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCwdUnder_ResolvesSymlinks pins R3: the scope match canonicalizes symlinks,
// so a macOS-style /tmp↔/private/tmp difference (here a real symlink) doesn't
// cause a stray miss.
func TestCwdUnder_ResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if !cwdUnder(filepath.Join(real, "sub"), link) {
		t.Errorf("scope given via symlink must match a recorded real path")
	}
	if !cwdUnder(link, real) {
		t.Errorf("recorded path via symlink must match a real scope")
	}
	if cwdUnder(real+"-other", real) {
		t.Errorf("a sibling dir sharing a prefix must not match")
	}
}
