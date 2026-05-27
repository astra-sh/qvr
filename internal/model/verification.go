package model

import "time"

// Verification status constants. These map to the agnostic trust model: a
// skill is "verified" when it carries a valid signature from a signer listed
// in trust.yaml, "untrusted" when signed by an unknown signer, "unverified"
// when no signature exists, "failed" when verification was attempted and
// rejected (tampered subtree, broken envelope, etc.).
const (
	StatusVerified   = "verified"
	StatusUntrusted  = "untrusted"
	StatusUnverified = "unverified"
	StatusFailed     = "failed"
)

// VerificationRecord captures the supply-chain provenance for a single
// installed skill. Every field beyond the subtree/provenance identity is a
// nullable slot populated by a later pipeline stage (scan, eval, card,
// sign). Phase 1 only fills SubtreeHash, TreeSHA, CommitSHA, Provenance and
// Status — later phases write into the optional pointers without needing a
// further lockfile version bump.
type VerificationRecord struct {
	// Identity of what was verified — the canonical subtree hash is the
	// subject every signature, attestation, and tamper check resolves to.
	SubtreeHash string `json:"subtreeHash"`
	TreeSHA     string `json:"treeSHA"`
	CommitSHA   string `json:"commitSHA"`

	// Provenance — where this skill was sourced from at verification time.
	Provenance ProvenanceRef `json:"provenance"`

	// Future-phase slots. nil until the owning phase ships.
	SkillCard   *ArtifactRef    `json:"skillCard,omitempty"`
	Scan        *ScanRef        `json:"scan,omitempty"`
	Eval        *EvalRef        `json:"eval,omitempty"`
	Signature   *SignatureBlock `json:"signature,omitempty"`
	Attestation *ArtifactRef    `json:"attestation,omitempty"`

	// Outcome of the most recent verification pass.
	Status     string    `json:"status"`
	Warnings   []string  `json:"warnings,omitempty"`
	PolicyRule string    `json:"policyRule,omitempty"`
	VerifiedAt time.Time `json:"verifiedAt"`
}

// ProvenanceRef records the upstream the skill came from. Empty registry
// fields are valid for Source == "subdir" (ad-hoc URL install) or
// Source == "link" (local symlink, no upstream).
type ProvenanceRef struct {
	RegistryName string    `json:"registryName,omitempty"`
	RegistryURL  string    `json:"registryURL,omitempty"`
	Ref          string    `json:"ref,omitempty"`
	Subpath      string    `json:"subpath,omitempty"`
	FetchedAt    time.Time `json:"fetchedAt"`
}

// ArtifactRef points at a JSON or YAML artifact alongside the skill,
// identified by its canonical sha256. Used for SKILLCARD.yaml,
// .quiver-attestation.json, and similar.
type ArtifactRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Schema string `json:"schema,omitempty"`
}

// ScanRef summarises a scanner pass. The full report lives on disk; only
// the digest and counts ride in the lockfile so `qvr lock verify` can
// detect drift without re-reading the report.
type ScanRef struct {
	ReportSHA      string         `json:"reportSHA"`
	ScannerVersion string         `json:"scannerVersion"`
	Counts         SeverityCounts `json:"counts"`
	Decision       string         `json:"decision"`
	SarifPath      string         `json:"sarifPath,omitempty"`
}

// EvalRef summarises an eval-harness pass. Scores is open-ended — metric
// IDs are decided by the harness, not pinned by the lockfile schema.
type EvalRef struct {
	ReportSHA      string             `json:"reportSHA"`
	HarnessVersion string             `json:"harnessVersion"`
	SuiteSHA       string             `json:"suiteSHA"`
	Scores         map[string]float64 `json:"scores,omitempty"`
	Passed         bool               `json:"passed"`
}

// SignatureBlock captures everything needed to re-verify a signature
// offline against a trusted public key. ManifestDigest duplicates the
// outer VerificationRecord.SubtreeHash so a SignatureBlock is
// self-contained for replay.
type SignatureBlock struct {
	Path           string `json:"path"`
	EnvelopeSHA    string `json:"envelopeSHA"`
	Algorithm      string `json:"algorithm"`
	SignerID       string `json:"signerID,omitempty"`
	PublicKeySHA   string `json:"publicKeySHA,omitempty"`
	ManifestDigest string `json:"manifestDigest"`
}

// SeverityCounts is the per-severity tally of scanner findings. Phase 2's
// scanner writers populate this; Phase 1 only reserves the type.
type SeverityCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}
