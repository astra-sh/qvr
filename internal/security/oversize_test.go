package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOversizeFileStillCatchesSecrets is the regression guard for
// the streaming-scan half of issue #44: the 1 MiB cap previously let
// a GitHub PAT smuggled into a >1 MiB file slip past every check.
// Even with the cap raised, an attacker can still pad beyond it; the
// streaming credential-prefix scan in WalkSkill must catch the secret
// regardless.
func TestOversizeFileStillCatchesSecrets(t *testing.T) {
	prev := SetMaxScanBytes(256)
	t.Cleanup(func() { SetMaxScanBytes(prev) })

	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "---\nname: padded\ndescription: leak hidden by padding\n---\n")
	body := "ghp_1234567890abcdefghijklmnopqrstuvwxyz\n" + strings.Repeat("x", 4096)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "leak.txt"), []byte(body), 0o644))

	res, err := New().Scan(context.Background(), nil, dir)
	require.NoError(t, err)
	var sawSecret, sawCoverage bool
	for _, f := range res.Findings {
		if f.Check == SecretsCheckName && f.File == "leak.txt" && f.RuleID == "SEC_GITHUB_PAT" {
			sawSecret = true
			assert.Equal(t, SeverityCritical, f.Severity)
			assert.Contains(t, f.Message, "streaming scan")
		}
		if f.RuleID == "COV_OVERSIZE" && f.File == "leak.txt" {
			sawCoverage = true
			assert.Equal(t, SeverityWarning, f.Severity,
				"COV_OVERSIZE must be warning so --fail-on warning gates on it (#44)")
		}
	}
	assert.True(t, sawSecret, "streaming scan must surface secrets hidden in oversize files; got %+v", res.Findings)
	assert.True(t, sawCoverage, "coverage finding must still fire alongside the streamed secret hit")
}

// TestCoverageFindingIsWarning replaces the older info-severity
// assertion. Issue #44: anything less than warning leaves the
// default fail-on=error blind to the bypass.
func TestCoverageFindingIsWarning(t *testing.T) {
	prev := SetMaxScanBytes(8)
	t.Cleanup(func() { SetMaxScanBytes(prev) })

	files := []FileEntry{
		{Path: "huge.txt", Truncated: true, Size: 1024},
	}
	findings := NewCoverageCheck().Run(context.Background(), nil, files)
	require.Len(t, findings, 1)
	assert.Equal(t, SeverityWarning, findings[0].Severity)
	assert.Contains(t, findings[0].Remediation, "--max-file-bytes")
}

// TestSetMaxScanBytesDisablesCap verifies that passing 0 to
// SetMaxScanBytes reads files of any size to completion (the
// "scan-everything" CI mode).
func TestSetMaxScanBytesDisablesCap(t *testing.T) {
	prev := SetMaxScanBytes(0)
	t.Cleanup(func() { SetMaxScanBytes(prev) })

	dir := t.TempDir()
	body := "ghp_1234567890abcdefghijklmnopqrstuvwxyz\n" + strings.Repeat("y", 200000)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "huge.txt"), []byte(body), 0o644))
	mustWriteFile(t, dir, "SKILL.md", "---\nname: x\ndescription: y\n---\n")

	entries, err := WalkSkill(dir)
	require.NoError(t, err)
	var huge *FileEntry
	for i := range entries {
		if entries[i].Path == "huge.txt" {
			huge = &entries[i]
		}
	}
	require.NotNil(t, huge)
	assert.False(t, huge.Truncated, "cap=0 must disable truncation regardless of size")
	assert.NotEmpty(t, huge.Content)
}

// TestSetMaxScanBytesClampsNegative just asserts the contract.
func TestSetMaxScanBytesClampsNegative(t *testing.T) {
	prev := SetMaxScanBytes(-5)
	t.Cleanup(func() { SetMaxScanBytes(prev) })
	assert.Equal(t, int64(0), CurrentMaxScanBytes())
}
