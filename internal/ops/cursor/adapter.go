// Package cursor is the first-party SkillOps adapter for Cursor. It
// implements ops.Adapter (parsing Cursor hook events) and ops.HookInstaller
// (managing ~/.cursor/hooks.json). Ported from inspo/gryph/agent/cursor/,
// with Quiver payload types and funnel-owned privacy/logging.
package cursor

import (
	"context"

	"github.com/raks097/quiver/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook cursor <type>`.
const AgentName = "cursor"

const displayName = "Cursor"

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
