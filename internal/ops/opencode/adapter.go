// Package opencode is the first-party SkillOps adapter for OpenCode. Unlike
// the JSON-hooks agents, OpenCode loads JS plugins, so installation drops an
// embedded quiver.js into ~/.config/opencode/plugins/ that shells out to
// `qvr _hook opencode <type>`. Ported from inspo/gryph/agent/opencode/.
package opencode

import (
	"context"

	"github.com/raks097/quiver/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook opencode <type>`.
const AgentName = "opencode"

const displayName = "OpenCode"

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
