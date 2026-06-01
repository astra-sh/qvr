// Package claudecode is the first-party SkillOps adapter for Claude Code.
// It implements ops.Adapter (parsing Claude Code hook events into canonical
// ops.Event records) and ops.HookInstaller (wiring qvr into
// ~/.claude/settings.json).
//
// Ported from inspo/gryph/agent/claudecode/, with two deliberate changes:
// payloads use Quiver's ops.* types, and privacy/logging concerns are left
// to the funnel (the adapter is a zero-value struct, never marking
// sensitivity or truncating itself).
package claudecode

import (
	"context"

	"github.com/raks097/quiver/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook claude-code <type>` and
// `qvr audit install-hooks --agent claude-code`.
const AgentName = "claude-code"

// displayName is the human-facing label.
const displayName = "Claude Code"

// Adapter implements both ops.Adapter and ops.HookInstaller. It holds no
// state — privacy and logging are the funnel's job.
type Adapter struct{}

// Name satisfies ops.Adapter and ops.HookInstaller.
func (a *Adapter) Name() string { return AgentName }

// DisplayName satisfies ops.HookInstaller.
func (a *Adapter) DisplayName() string { return displayName }

// ParseEvent satisfies ops.Adapter. It translates one Claude Code hook
// invocation into a canonical event.
func (a *Adapter) ParseEvent(_ context.Context, hookType string, rawData []byte) (*ops.Event, error) {
	return parseHookEvent(hookType, rawData)
}

func init() {
	ops.Register(&Adapter{})
}
