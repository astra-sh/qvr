package derive

import reg "github.com/astra-sh/qvr/internal/registry"

// This file is the ONE place agent-facing labels live, so the rest of the
// pipeline stays agent-agnostic. Two things are centralized:
//
//   - agentProfile: the per-agent display strings for the clean span tree
//     (model-span name, root-turn name, integration tag). Adding an agent — or
//     adapting one whose transcript behavior changes (e.g. a future Claude Code
//     that records resolved store paths like codex) — is a single table edit
//     here; no deriver or enrichment branch keys on the agent name.
//
//   - SkillVersionLabel: the human version label every read surface shows, with
//     a graceful "unknown" fallback so an unprovable or pre-snapshot run reads
//     honestly instead of blank.
//
// Note on the modularity claim: enrichment (enrich.go) already resolves identity
// from EVIDENCE (is the version sha in the recorded bytes?), never from the agent
// name — so an agent that starts recording transcript-pinned paths is picked up
// automatically. This table only governs DISPLAY.

// agentProfile holds the display strings for one agent's span tree.
type agentProfile struct {
	model       string // model-span title ("Claude")
	rootName    string // root turn-span title ("Claude Code Turn")
	integration string // qvr.integration tag ("claude-code")
}

// agentProfiles is keyed by canonical target name (model.CanonicalTarget), so
// aliases collapse to one entry. To support a new agent, add a row.
var agentProfiles = map[string]agentProfile{
	"claude": {model: "Claude", rootName: "Claude Code Turn", integration: "claude-code"},
	"codex":  {model: "Codex", rootName: "Codex Turn", integration: "codex"},
}

// profileFor returns the display profile for an agent (by canonical name), with a
// generic fallback so an unlisted agent still derives a clean, if plainer, tree.
func profileFor(agent string) agentProfile {
	if p, ok := agentProfiles[canonicalAgent(agent)]; ok {
		return p
	}
	return agentProfile{model: agent, rootName: agent + " Turn", integration: agent}
}

// IntegrationLabel is the qvr.integration tag for an agent — the single source of
// truth, so a span's integration always aligns with its canonical agent_name
// (issue: agent_name vs integration drift). Exported for read surfaces that want
// to label the integration from a session's agent_name.
func IntegrationLabel(agent string) string { return profileFor(agent).integration }

// UnknownVersion is the graceful label for a run whose version could not be
// proven (a pre-snapshot run, or one whose load evidence resolves to nothing
// trustworthy). It still buckets by its content coordinate; only the human label
// is unknown.
const UnknownVersion = "unknown"

// SkillVersionLabel renders the human version label from a span's proven
// identity, most specific first: the ref/version when proven; else the short
// commit (a real, run-immutable id, just without a human ref); else "unknown".
// This is the shared labeler for compare, sessions, export, and the UI so they
// never disagree.
func SkillVersionLabel(version, commit string) string {
	if version != "" {
		return version
	}
	if commit != "" {
		return reg.ShortSHA(commit)
	}
	return UnknownVersion
}
