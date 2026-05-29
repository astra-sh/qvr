package cmd

import (
	"strings"
	"testing"
)

// TestConfigValidator_BlockSeverityAcceptsScannerVocab pins the alignment
// from bug #58: scanner emits info|warning|error|critical, so config must
// accept those values (and only those — no "none/high/medium/low" decoys).
func TestConfigValidator_BlockSeverityAcceptsScannerVocab(t *testing.T) {
	v, ok := configValueValidators["security.block_severity"]
	if !ok {
		t.Fatal("no validator for security.block_severity")
	}
	for _, sev := range []string{"info", "warning", "error", "critical"} {
		if _, err := v(sev); err != nil {
			t.Errorf("%q should validate: %v", sev, err)
		}
	}
	// Old vocab from before #58 — these must now be rejected.
	for _, sev := range []string{"high", "medium", "low", "none"} {
		if _, err := v(sev); err == nil {
			t.Errorf("%q should be rejected (legacy vocab from before #58)", sev)
		}
	}
}

// TestConfigValidator_BlockSeverityErrorMessageListsScannerVocab makes sure
// the rejection message tells the user the right values to try.
func TestConfigValidator_BlockSeverityErrorMessageListsScannerVocab(t *testing.T) {
	v := configValueValidators["security.block_severity"]
	_, err := v("nope")
	if err == nil {
		t.Fatal("expected error on bogus severity")
	}
	for _, want := range []string{"critical", "error", "warning", "info"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q should mention %q", err.Error(), want)
		}
	}
	for _, leg := range []string{"high", "medium", "low"} {
		if strings.Contains(err.Error(), leg) {
			t.Errorf("error message %q should NOT mention legacy vocab %q", err.Error(), leg)
		}
	}
}
