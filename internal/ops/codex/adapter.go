// Package codex is the first-party SkillOps adapter for Codex CLI. It
// implements ops.Adapter and ops.HookInstaller over ~/.codex/hooks.json.
// Ported from inspo/gryph/agent/codex/.
//
// Scope note: Quiver ships the hooks.json mode only. Codex CLI's alternate
// config.toml event-stream / --trace-fd mode is a documented deferred
// follow-up.
package codex

import (
	"context"

	"github.com/raks097/quiver/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook codex <type>`.
const AgentName = "codex"

const displayName = "Codex CLI"

// Adapter implements ops.Adapter and ops.HookInstaller.
type Adapter struct{}

func (a *Adapter) Name() string        { return AgentName }
func (a *Adapter) DisplayName() string { return displayName }

func (a *Adapter) ParseEvent(_ context.Context, hookType string, rawData []byte) (*ops.Event, error) {
	return parseHookEvent(hookType, rawData)
}

func init() {
	ops.Register(&Adapter{})
}
