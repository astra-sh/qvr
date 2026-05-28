package security

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCoverageReportsOversizeFiles is the regression guard for
// issue #34: padding any file over the 1 MiB cap previously turned
// every detector blind without surfacing the gap. The coverage check
// must emit at least a warning finding for every oversize file
// (severity raised from info per issue #44 so --fail-on warning gates
// on it).
func TestCoverageReportsOversizeFiles(t *testing.T) {
	files := []FileEntry{
		{Path: "tiny.md", Content: "hi\n", Size: 3},
		{Path: "huge.txt", Truncated: true, Size: maxScanBytes + 1},
	}
	findings := NewCoverageCheck().Run(context.Background(), nil, files)
	require.Len(t, findings, 1, "exactly one finding (the oversize file)")
	assert.Equal(t, "COV_OVERSIZE", findings[0].RuleID)
	assert.Equal(t, SeverityWarning, findings[0].Severity)
	assert.Equal(t, "huge.txt", findings[0].File)
	assert.Contains(t, findings[0].Message, "exceeds the")
}

func TestCoverageReportsBinaryFiles(t *testing.T) {
	files := []FileEntry{
		{Path: "blob.bin", IsBinary: true, Size: 2048},
	}
	findings := NewCoverageCheck().Run(context.Background(), nil, files)
	require.Len(t, findings, 1)
	assert.Equal(t, "COV_BINARY", findings[0].RuleID)
	assert.Equal(t, SeverityInfo, findings[0].Severity)
}

func TestCoverageNoFindingsOnCleanScan(t *testing.T) {
	files := []FileEntry{{Path: "SKILL.md", Content: "# clean\n", Size: 8}}
	assert.Empty(t, NewCoverageCheck().Run(context.Background(), nil, files))
}

// TestCoverageOversizeFileShowsInScanResult is an end-to-end guard:
// walking a real temp dir with a >1MiB file must surface a finding,
// not return a quiet "scan clean".
func TestCoverageOversizeFileShowsInScanResult(t *testing.T) {
	// Use a tiny cap so the test fixture stays small and fast; the
	// real default is 10 MiB and we don't need to generate that much
	// data just to prove the oversize path fires.
	prev := SetMaxScanBytes(64)
	t.Cleanup(func() { SetMaxScanBytes(prev) })

	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "---\nname: huge\ndescription: oversize regression\n---\n")
	mustWriteFile(t, dir, "huge.txt", strings.Repeat("a", int(maxScanBytes+1)))

	res, err := New().Scan(context.Background(), nil, dir)
	require.NoError(t, err)
	var found bool
	for _, f := range res.Findings {
		if f.RuleID == "COV_OVERSIZE" && f.File == "huge.txt" {
			found = true
		}
	}
	assert.True(t, found, "scan must emit COV_OVERSIZE for the oversize file; got %+v", res.Findings)
}
