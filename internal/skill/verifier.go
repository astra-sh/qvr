package skill

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/model"
)

// Per-entry verification status codes used by VerifySingleEntry. These
// are surfaced unchanged in `qvr lock verify` JSON output, so they're
// part of the public CLI contract.
const (
	VerifyStatusOK         = "ok"
	VerifyStatusDrift      = "drift"
	VerifyStatusUnverified = "unverified"
	VerifyStatusMissing    = "missing"
	VerifyStatusLink       = "link"
	VerifyStatusFailed     = "failed"
)

// VerifyEntryResult is one row of `qvr lock verify` output.
type VerifyEntryResult struct {
	Name        string            `json:"name"`
	Status      string            `json:"status"`
	SubtreeHash string            `json:"subtreeHash,omitempty"`
	Drift       []VerifyDriftItem `json:"drift,omitempty"`
	Message     string            `json:"message,omitempty"`
}

// VerifyDriftItem names one field that diverged between recorded and
// computed state for an entry.
type VerifyDriftItem struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// VerifySingleEntry recomputes the canonical subtree hash for entry and
// compares it against the recorded VerificationRecord. Returns a
// structured result that callers (the CLI, integration tests, future
// daemons) classify into pass/fail per their own policy.
//
// Phase 1 detects drift at the subtree/tree/commit level; signature and
// scan verification are wired in later phases by extending the result
// with additional Drift entries.
func VerifySingleEntry(entry *model.LockEntry) VerifyEntryResult {
	res := VerifyEntryResult{Name: entry.Name}
	if entry.Source == "link" {
		res.Status = VerifyStatusLink
		res.Message = "link install — no upstream to verify"
		return res
	}
	if entry.Worktree == "" {
		res.Status = VerifyStatusMissing
		res.Message = "worktree path is empty"
		return res
	}
	if _, err := os.Stat(entry.Worktree); err != nil {
		res.Status = VerifyStatusMissing
		res.Message = fmt.Sprintf("worktree not found: %v", err)
		return res
	}
	id, err := canonical.HashSubtree(entry.Worktree, entry.Path)
	if err != nil {
		res.Status = VerifyStatusFailed
		res.Message = err.Error()
		return res
	}
	res.SubtreeHash = id.SubtreeHash

	if entry.Verification == nil || entry.Verification.SubtreeHash == "" {
		res.Status = VerifyStatusUnverified
		res.Message = "no recorded subtree hash (legacy entry — run `qvr lock upgrade`)"
		return res
	}

	rec := entry.Verification
	var drift []VerifyDriftItem
	if rec.SubtreeHash != id.SubtreeHash {
		drift = append(drift, VerifyDriftItem{Field: "subtreeHash", Expected: rec.SubtreeHash, Actual: id.SubtreeHash})
	}
	if rec.TreeSHA != "" && rec.TreeSHA != id.TreeSHA {
		drift = append(drift, VerifyDriftItem{Field: "treeSHA", Expected: rec.TreeSHA, Actual: id.TreeSHA})
	}
	if rec.CommitSHA != "" && rec.CommitSHA != id.CommitSHA {
		drift = append(drift, VerifyDriftItem{Field: "commitSHA", Expected: rec.CommitSHA, Actual: id.CommitSHA})
	}
	if len(drift) == 0 {
		res.Status = VerifyStatusOK
		return res
	}
	res.Status = VerifyStatusDrift
	res.Drift = drift
	return res
}
