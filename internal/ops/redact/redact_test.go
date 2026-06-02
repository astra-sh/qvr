package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBytes_MasksSecretsKeepsReasoning(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		mustMask  string // a secret substring that must NOT survive
		mustKeep  []string
		stillJSON bool
	}{
		{
			name:      "aws key in reasoning text",
			in:        `{"type":"text","text":"I'll use the key AKIAIOSFODNN7EXAMPLE to auth, then proceed"}`,
			mustMask:  "AKIAIOSFODNN7EXAMPLE",
			mustKeep:  []string{"I'll use the key", "to auth, then proceed"},
			stillJSON: true,
		},
		{
			name:      "openai key at end of json string",
			in:        `{"command":"export OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz0123456789"}`,
			mustMask:  "sk-abcdefghijklmnopqrstuvwxyz0123456789",
			mustKeep:  []string{"export", "OPENAI_API_KEY="},
			stillJSON: true,
		},
		{
			name:      "password assignment keeps the key",
			in:        `{"text":"set password=hunter2horse in the config file"}`,
			mustMask:  "hunter2horse",
			mustKeep:  []string{"password=", "in the config file"},
			stillJSON: true,
		},
		{
			name:      "no secret is unchanged",
			in:        `{"type":"thinking","thinking":"let me reason about the algorithm carefully"}`,
			mustMask:  "",
			mustKeep:  []string{"let me reason about the algorithm carefully"},
			stillJSON: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := string(Bytes([]byte(c.in)))

			if c.mustMask != "" && strings.Contains(out, c.mustMask) {
				t.Errorf("secret survived redaction:\n %s", out)
			}
			for _, k := range c.mustKeep {
				if !strings.Contains(out, k) {
					t.Errorf("redaction dropped reasoning %q from:\n %s", k, out)
				}
			}
			if c.stillJSON {
				var v any
				if err := json.Unmarshal([]byte(out), &v); err != nil {
					t.Errorf("redaction broke JSON: %v\n %s", err, out)
				}
			}
			if c.mustMask == "" && out != c.in {
				t.Errorf("clean input was modified:\n in:  %s\n out: %s", c.in, out)
			}
		})
	}
}

func TestBytes_MaskAppearsWhenSecretPresent(t *testing.T) {
	out := string(Bytes([]byte(`token: ghp_0123456789abcdefghijklmnopqrstuvwxyz`)))
	if !strings.Contains(out, Marker) {
		t.Errorf("expected %s marker; got %s", Marker, out)
	}
}
