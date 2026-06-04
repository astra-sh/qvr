package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestParents_RejectUnknownSubcommand is the #169 regression: the #120 fix
// (reject a typo'd subcommand with a non-zero exit instead of silently printing
// help and exiting 0) had landed for cache/completion but missed the `audit`
// and `registry` parents — whose subcommands mutate config. Every pure-parent
// command must error on an unknown positional and stay quiet (nil → help) with
// none.
func TestParents_RejectUnknownSubcommand(t *testing.T) {
	parents := map[string]*cobra.Command{
		"audit":    auditCmd,
		"registry": registryCmd,
		"cache":    cacheCmd,
	}
	for name, cmd := range parents {
		t.Run(name+"/unknown", func(t *testing.T) {
			err := cmd.RunE(cmd, []string{"bogus"})
			if err == nil {
				t.Fatalf("%s.RunE returned nil on unknown subcommand 'bogus' (issue #169)", name)
			}
			if !strings.Contains(err.Error(), "unknown command") {
				t.Errorf("error = %v; want substring 'unknown command'", err)
			}
		})
		t.Run(name+"/noargs", func(t *testing.T) {
			if err := cmd.RunE(cmd, nil); err != nil {
				t.Errorf("%s.RunE returned %v on no args; want nil (prints help)", name, err)
			}
		})
	}
}
