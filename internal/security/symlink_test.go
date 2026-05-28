package security

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPermissionsIgnoresSymlinkExecBit is the regression guard for
// issue #40: every symlink on macOS/Linux reports mode 0o777 via
// lstat, so the old check produced a "marked executable" warning for
// every link in the skill (including dangling and loop links).
func TestPermissionsIgnoresSymlinkExecBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "---\nname: syms\ndescription: symlink coverage\n---\n")
	require.NoError(t, os.Symlink("SKILL.md", filepath.Join(dir, "inbound")))
	require.NoError(t, os.Symlink("/no/such/path", filepath.Join(dir, "dangling")))
	require.NoError(t, os.Symlink("loop-b", filepath.Join(dir, "loop-a")))
	require.NoError(t, os.Symlink("loop-a", filepath.Join(dir, "loop-b")))

	files, err := WalkSkill(dir)
	require.NoError(t, err)
	for _, f := range files {
		if !f.IsSymlink {
			continue
		}
		assert.False(t, f.Executable(),
			"symlink %s must not be reported as executable (lstat mode is not file mode)", f.Path)
	}

	findings := NewPermissionsCheck().Run(context.Background(), nil, files)
	for _, f := range findings {
		if f.RuleID == "PERM_EXEC_BIT" {
			t.Errorf("issue #40 regressed: symlink false-positive for %s", f.File)
		}
	}
}

func TestPermissionsReportsBrokenSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "---\nname: brk\ndescription: dangling\n---\n")
	require.NoError(t, os.Symlink("/nope/missing", filepath.Join(dir, "dangling")))
	require.NoError(t, os.Symlink("loop-b", filepath.Join(dir, "loop-a")))
	require.NoError(t, os.Symlink("loop-a", filepath.Join(dir, "loop-b")))

	files, err := WalkSkill(dir)
	require.NoError(t, err)
	findings := NewPermissionsCheck().Run(context.Background(), nil, files)

	broken := map[string]bool{}
	for _, f := range findings {
		if f.RuleID == "PERM_SYMLINK_BROKEN" {
			broken[f.File] = true
			assert.Equal(t, SeverityInfo, f.Severity)
		}
	}
	assert.True(t, broken["dangling"], "dangling symlink must be reported")
	assert.True(t, broken["loop-a"], "cyclic symlink must be reported")
	assert.True(t, broken["loop-b"], "cyclic symlink must be reported")
}

func TestPermissionsExecBitStillFiresOnRealFiles(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "---\nname: exec\ndescription: real exec\n---\n")
	target := filepath.Join(dir, "run.sh")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\necho hi\n"), 0o755))

	files, err := WalkSkill(dir)
	require.NoError(t, err)
	findings := NewPermissionsCheck().Run(context.Background(), nil, files)
	var hit bool
	for _, f := range findings {
		if f.RuleID == "PERM_EXEC_BIT" && f.File == "run.sh" {
			hit = true
		}
	}
	assert.True(t, hit, "PERM_EXEC_BIT must still fire on a real executable file")
}
