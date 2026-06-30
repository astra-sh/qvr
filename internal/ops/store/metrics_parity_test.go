package store

import (
	"context"
	"testing"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"
)

// seedSkillSpan stores one SKILL span for a session with the given agent and raw
// skill.* attributes — enough to exercise the content-cohort coordinate.
func seedSkillSpan(t *testing.T, st Store, agent, attrs string) {
	t.Helper()
	sid := uuid.New()
	span := &SpanRow{
		SpanID: "s_" + sid.String()[:8], TraceID: "tr_" + sid.String()[:8], SessionID: sid,
		AgentName: agent, Kind: "SKILL", Name: "slugify-title", StartMs: 100, EndMs: 100,
		Attributes: attrs,
	}
	meta := &SessionMetaRow{
		SessionID: sid, AgentName: agent, SourceSessionID: sid.String(),
		StartedMs: 100, EndedMs: 100, Skills: []string{"slugify-title"},
	}
	if err := st.ReplaceSessionDerivation(context.Background(), meta, []*SpanRow{span}); err != nil {
		t.Fatalf("seed %s: %v", agent, err)
	}
}

// TestSkillContentRollup_CrossAgentVersionParity is the prime-objective guard:
// the SAME installed skill version, loaded by different agents through different
// mechanisms, must fall into ONE content cohort. A claude Skill-tool load records
// content_hash as the loaded-body digest (agent-specific) but enriches to the
// proven subtree_hash; a codex SKILL.md read records content_hash AS that same
// subtree_hash. They share only the subtree_hash — so the cohort coordinate must
// key on it, not on the divergent content_hash, or one version splits in two.
func TestSkillContentRollup_CrossAgentVersionParity(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	const subtree = "sha256:afcdd32beb92f22893c8ba1624c8858a2a053e7d787ace34234be05204869c80"

	// claude: body-digest content_hash (differs from subtree), proven subtree_hash.
	seedSkillSpan(t, st, "claude",
		`{"skill.name":"slugify-title","skill.activation":"tool",`+
			`"skill.content_hash":"sha256:3ee9aca80ecc2adc2bbd7d65b305993727af0aa5231f4e40d07e6e53e43ca939",`+
			`"skill.commit":"2c20399","skill.subtree_hash":"`+subtree+`","qvr.outcome":"success"}`)
	// codex: content_hash already IS the subtree_hash (transcript-pinned path).
	seedSkillSpan(t, st, "codex",
		`{"skill.name":"slugify-title","skill.activation":"path",`+
			`"skill.content_hash":"`+subtree+`",`+
			`"skill.commit":"2c20399","skill.subtree_hash":"`+subtree+`","qvr.outcome":"success"}`)
	// codex implicit: no body read; proven via catalog path.
	seedSkillSpan(t, st, "codex",
		`{"skill.name":"slugify-title","skill.activation":"implicit",`+
			`"skill.content_hash":"`+subtree+`",`+
			`"skill.commit":"2c20399","skill.subtree_hash":"`+subtree+`","qvr.outcome":"success"}`)

	cohorts, err := st.SkillContentRollup(ctx, &MetricsFilter{Skill: "slugify-title"})
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(cohorts) != 1 {
		var got []string
		for _, c := range cohorts {
			got = append(got, c.ContentHash)
		}
		t.Fatalf("same version across agents must form ONE cohort, got %d: %v", len(cohorts), got)
	}
	if cohorts[0].Sessions != 3 {
		t.Errorf("cohort should hold all 3 runs of the version, got %d sessions", cohorts[0].Sessions)
	}
}
