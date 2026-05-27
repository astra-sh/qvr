package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// QvrSignatureVersion is the on-disk envelope schema version.
const QvrSignatureVersion = "qvr-signature-v1"

// QvrSignatureArtifactType identifies what a qvr.sig envelope covers. The
// only artifact type Phase 1 anticipates is a skill directory; future
// types (e.g. registry-index attestations) get their own constant.
const QvrSignatureArtifactType = "qvr.skill.directory"

// QvrSignature is the on-disk signature envelope, written as `qvr.sig` at
// the root of a signed skill subtree. The schema is frozen here so Phase
// 1 lockfile entries carry the right field names even though no signer
// implementation exists yet — Phase 5 fills in PublicKey and Signature
// without changing the layout.
//
// What gets signed: the JCS serialisation of this struct with `Signature`
// cleared. Hashing the envelope (not just ManifestDigest) binds the
// metadata — signed_at, algorithm, artifact_type — into the signature so
// a hostile rebundler can't reuse a signed digest under different terms.
type QvrSignature struct {
	Version        string    `json:"version"`
	Algorithm      string    `json:"algorithm"`
	Hash           string    `json:"hash"`
	ArtifactType   string    `json:"artifact_type"`
	SignedAt       time.Time `json:"signed_at"`
	ManifestDigest string    `json:"manifest_digest"`
	PublicKey      string    `json:"public_key"`
	Signature      string    `json:"signature"`
}

// SigningPayload returns the JCS bytes that a signer should sign over:
// the envelope minus the Signature field. Verifiers reconstruct the same
// bytes for verification.
func (q QvrSignature) SigningPayload() ([]byte, error) {
	q.Signature = ""
	return JCS(q)
}

// EnvelopeDigest returns sha256 over the full envelope (including
// Signature), suitable for the SignatureBlock.EnvelopeSHA lockfile field.
// Drift between a recorded EnvelopeSHA and a recomputed one signals that
// the on-disk qvr.sig has been swapped since install — tamper-evidence
// for the wrapper itself.
func (q QvrSignature) EnvelopeDigest() (string, error) {
	raw, err := JCS(q)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
