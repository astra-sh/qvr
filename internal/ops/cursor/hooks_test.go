package cursor

import (
	"os"
	"path/filepath"
	"testing"
)

// setupCursorHome isolates $HOME and $QVR_HOME and creates ~/.cursor so
// Detect reports true.
func setupCursorHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QVR_HOME", filepath.Join(home, ".quiver"))
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatalf("mkdir .cursor: %v", err)
	}
	return home
}
