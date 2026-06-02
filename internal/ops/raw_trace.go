package ops

import (
	"time"

	"github.com/google/uuid"
)

// RawTrace is one verbatim slice of an agent's native output — either a single
// line from its transcript/rollout file or a raw hook payload — stored exactly
// as the agent produced it. There is intentionally NO typed payload, action
// enum, or schema envelope here: the whole point is that Raw is byte-for-byte
// what the agent wrote, so any downstream view can be derived (and re-derived)
// without ever having lost information at capture time.
type RawTrace struct {
	ID               uuid.UUID
	AgentName        string
	SessionID        uuid.UUID
	AgentSessionID   string
	Source           string // RawSourceTranscript | RawSourceHookPayload
	SourcePath       string // transcript file path; "" for hook payloads
	WorkingDirectory string // cwd reported by the hook (drives project scoping)
	HookType         string // hook event name; "" for transcript rows
	ByteOffset       int64  // start offset of this line within SourcePath
	Seq              int    // monotonic per session, capture order
	CapturedAt       time.Time
	Raw              []byte // verbatim native bytes, untouched
}

// Source discriminators for RawTrace.Source.
const (
	// RawSourceTranscript is a single line tailed from the agent's own
	// transcript/rollout JSONL — the richest record, carrying reasoning,
	// assistant text, user input, and tool calls as the agent logged them.
	RawSourceTranscript = "transcript"

	// RawSourceHookPayload is the raw JSON the harness delivered to the hook
	// on stdin. Captured alongside the transcript so harness-level events that
	// never land in the transcript (notifications, permission decisions, the
	// exact PostToolUse response) are not lost.
	RawSourceHookPayload = "hook_payload"
)
