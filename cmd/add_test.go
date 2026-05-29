package cmd

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/raks097/quiver/internal/skill"
)

// TestBuildAddJSONEnvelope locks the JSON shape `qvr add --output json` emits
// for each of the three outcomes the bug #54 fix has to cover:
//
//   - all-success: {"installed": [...]}, no `error` key
//   - all-fail:    {"installed": [], "error": "..."}
//   - partial:     {"installed": [...], "error": "..."}
//
// The key shape promise is that `installed` is always an array (never null) so
// `jq '.installed[]'` works uniformly. Before #54 the all-fail case emitted
// the bare literal `null`.
func TestBuildAddJSONEnvelope(t *testing.T) {
	one := []*skill.InstallResult{{Name: "tdd", Version: "main"}}

	cases := []struct {
		name    string
		results []*skill.InstallResult
		err     error
		want    string
	}{
		{
			name:    "all-success",
			results: one,
			err:     nil,
			want:    `{"installed":[{"name":"tdd","registry":"","version":"main","worktree":"","targets":null,"commit":""}]}`,
		},
		{
			name:    "all-fail",
			results: nil,
			err:     errors.New("nope"),
			want:    `{"installed":[],"error":"nope"}`,
		},
		{
			name:    "partial",
			results: one,
			err:     errors.New("one-failed"),
			want:    `{"installed":[{"name":"tdd","registry":"","version":"main","worktree":"","targets":null,"commit":""}],"error":"one-failed"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := buildAddJSONEnvelope(tc.results, tc.err)
			b, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Fatalf("envelope mismatch\nwant: %s\ngot:  %s", tc.want, b)
			}
		})
	}
}
