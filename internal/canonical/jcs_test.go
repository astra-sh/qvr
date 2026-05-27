package canonical_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/canonical"
)

func TestJCS_objectKeysSorted(t *testing.T) {
	in := map[string]any{
		"b": 2,
		"a": 1,
		"c": 3,
	}
	got, err := canonical.JCS(in)
	if err != nil {
		t.Fatalf("JCS: %v", err)
	}
	want := `{"a":1,"b":2,"c":3}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestJCS_nestedObjectKeysSorted(t *testing.T) {
	in := map[string]any{
		"outer": map[string]any{
			"z": "last",
			"a": "first",
		},
	}
	got, err := canonical.JCS(in)
	if err != nil {
		t.Fatalf("JCS: %v", err)
	}
	want := `{"outer":{"a":"first","z":"last"}}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestJCS_arrayOrderPreserved(t *testing.T) {
	in := []any{3, 1, 2}
	got, err := canonical.JCS(in)
	if err != nil {
		t.Fatalf("JCS: %v", err)
	}
	if string(got) != `[3,1,2]` {
		t.Errorf("got %s, want [3,1,2]", got)
	}
}

func TestJCS_primitives(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "null"},
		{true, "true"},
		{false, "false"},
		{42, "42"},
		{-7, "-7"},
		{"hello", `"hello"`},
		{"a\"b", `"a\"b"`},
		{[]any{}, `[]`},
		{map[string]any{}, `{}`},
	}
	for _, c := range cases {
		got, err := canonical.JCS(c.in)
		if err != nil {
			t.Errorf("JCS(%v): %v", c.in, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("JCS(%v): got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestJCS_noInsignificantWhitespace(t *testing.T) {
	in := map[string]any{"a": map[string]any{"b": []any{1, 2}}}
	got, err := canonical.JCS(in)
	if err != nil {
		t.Fatalf("JCS: %v", err)
	}
	for _, r := range string(got) {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			t.Errorf("whitespace in canonical output: %q", got)
			break
		}
	}
}

func TestJCS_deterministicAcrossCalls(t *testing.T) {
	in := map[string]any{
		"version":         "qvr-signature-v1",
		"algorithm":       "ed25519",
		"hash":            "sha256",
		"signed_at":       "2026-05-27T12:00:00Z",
		"manifest_digest": "sha256:abc",
	}
	first, _ := canonical.JCS(in)
	second, _ := canonical.JCS(in)
	if string(first) != string(second) {
		t.Errorf("non-deterministic JCS:\n  %s\n  %s", first, second)
	}
}

func TestQvrSignature_signingPayloadExcludesSignature(t *testing.T) {
	env := canonical.QvrSignature{
		Version:        canonical.QvrSignatureVersion,
		Algorithm:      "ed25519",
		Hash:           "sha256",
		ArtifactType:   canonical.QvrSignatureArtifactType,
		SignedAt:       time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC),
		ManifestDigest: "sha256:abc",
		PublicKey:      "pk",
		Signature:      "sig-should-be-stripped",
	}
	payload, err := env.SigningPayload()
	if err != nil {
		t.Fatalf("SigningPayload: %v", err)
	}
	if strings.Contains(string(payload), "sig-should-be-stripped") {
		t.Errorf("signing payload still contains signature bytes: %s", payload)
	}
	// Re-running with the signature changed must produce identical bytes —
	// proof the field is stripped, not just stringified.
	env2 := env
	env2.Signature = "different-sig"
	payload2, _ := env2.SigningPayload()
	if string(payload) != string(payload2) {
		t.Errorf("signing payload depends on Signature field — should be excluded")
	}
}

func TestQvrSignature_envelopeDigestStable(t *testing.T) {
	env := canonical.QvrSignature{
		Version:        canonical.QvrSignatureVersion,
		Algorithm:      "ed25519",
		Hash:           "sha256",
		ArtifactType:   canonical.QvrSignatureArtifactType,
		SignedAt:       time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC),
		ManifestDigest: "sha256:abc",
	}
	d1, err := env.EnvelopeDigest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	d2, _ := env.EnvelopeDigest()
	if d1 != d2 {
		t.Errorf("envelope digest non-deterministic: %s vs %s", d1, d2)
	}
	if !strings.HasPrefix(d1, "sha256:") {
		t.Errorf("envelope digest missing prefix: %s", d1)
	}
	// Sanity: re-computing manually should match.
	raw, _ := canonical.JCS(env)
	sum := sha256.Sum256(raw)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if d1 != want {
		t.Errorf("envelope digest does not match JCS+sha256: got %s, want %s", d1, want)
	}
}
