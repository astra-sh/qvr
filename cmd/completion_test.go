package cmd

import (
	"strings"
	"testing"
)

// TestCompletionCmd_UnknownShellErrors is the #120 regression: pre-fix
// `qvr completion garbage` fell through to cobra's auto-generated parent
// help with exit 0, so a CI script like
//
//	qvr completion "$SHELL_KIND" > /etc/bash_completion.d/qvr
//
// silently installed an empty file when $SHELL_KIND was unset. The fix
// re-materialises the completion command in init() and overrides its
// RunE to mirror lockCmd's "unknown command" handling.
func TestCompletionCmd_UnknownShellErrors(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() != "completion" {
			continue
		}
		if c.RunE == nil {
			t.Fatal("completion command has no RunE — init() override didn't run")
		}
		err := c.RunE(c, []string{"garbage"})
		if err == nil {
			t.Fatal("completion RunE returned nil on unknown shell 'garbage'")
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("error = %v; want substring 'unknown command'", err)
		}
		return
	}
	t.Fatal("completion command not found on rootCmd — InitDefaultCompletionCmd not called")
}
