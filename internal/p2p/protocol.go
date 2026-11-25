// Path: internal/p2p/protocol.go | Author: GLM (Z.ai) for the owner | 2026-11-25
// What/why: the signaling envelope of F11 and its fail-closed validation.
// The caps mirror web/pages/js/platform/p2p/rules.js (SIGNAL_MSG_MAX_BYTES,
// SDP_MAX_BYTES, ICE_CANDIDATE_MAX_BYTES): one protocol, one set of limits,
// enforced on BOTH ends, so a hostile client cannot talk either side into a
// larger transfer of trust.
//
// G-F11-1: envelopes carry no lease, owner or worker identifiers — only the
// relay-verified app binding the server already proved from the lease token
// (F10). The browser never learns another tab's lease or worker id.
package p2p

import (
	"fmt"
	"regexp"
)

// Caps, mirroring rules.js. Keep both sides in lockstep.
const (
	// MaxSDPBytes: one SDP blob (offers/answers are non-trickle: full SDP).
	MaxSDPBytes = 16 * 1024
	// MaxCandidateBytes: one ICE candidate line.
	MaxCandidateBytes = 2 * 1024
	// MaxEnvelopeBytes: the whole JSON envelope on the wire.
	MaxEnvelopeBytes = 64 * 1024
	// MaxQueuedPerPeer: relay queue bound per peer (backpressure, not storage).
	MaxQueuedPerPeer = 32
	// MessageTTLSeconds: relayed envelopes die after this long.
	MessageTTLSeconds = 60
)

var callIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,128}$`)

// Envelope types of the wire protocol.
const (
	TypeOffer     = "offer"
	TypeAnswer    = "answer"
	TypeCandidate = "candidate"
	TypeBye       = "bye"
)

// Envelope is one signaling message. The JSON field names are the contract
// with web/pages/js/platform/p2p/signaling.js (validateEnvelope).
type Envelope struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	CallID    string `json:"callId"`
	SDP       string `json:"sdp,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

// ValidateEnvelope enforces the caps. Offers are SHA-256 addressed (a module
// digest); results, receipts and inputs have no envelope representation at
// all: results and receipts always go to the server.
func ValidateEnvelope(e *Envelope) error {
	if e == nil || e.V != 1 {
		return fmt.Errorf("envelope version must be 1")
	}
	if !callIDRe.MatchString(e.CallID) {
		return fmt.Errorf("envelope callId malformed")
	}
	switch e.Type {
	case TypeOffer, TypeAnswer:
		if len(e.SDP) == 0 || len(e.SDP) > MaxSDPBytes {
			return fmt.Errorf("envelope sdp size out of range")
		}
		for _, r := range e.SDP {
			if r == 0 {
				return fmt.Errorf("envelope sdp contains NUL")
			}
		}
	case TypeCandidate:
		if len(e.Candidate) == 0 || len(e.Candidate) > MaxCandidateBytes {
			return fmt.Errorf("envelope candidate size out of range")
		}
	case TypeBye:
		// no payload
	default:
		return fmt.Errorf("envelope type %q unknown", e.Type)
	}
	if e.Digest != "" && !digestRe.MatchString(e.Digest) {
		return fmt.Errorf("envelope digest malformed")
	}
	if e.Type == TypeOffer && !digestRe.MatchString(e.Digest) {
		return fmt.Errorf("offer envelope requires a sha256 digest")
	}
	return nil
}

var digestRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateDigest enforces the SHA-256 hex digest shape shared with rules.js
// (SHA256_HEX_RE) and cache.js.
func ValidateDigest(raw string) error {
	if !digestRe.MatchString(raw) {
		return fmt.Errorf("digest must be 64 hex chars")
	}
	return nil
}

var peerIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,128}$`)

// ValidatePeerID enforces the random session peer id shape the client
// generates (crypto.randomUUID or the documented fallback).
func ValidatePeerID(raw string) error {
	if !peerIDRe.MatchString(raw) {
		return fmt.Errorf("peerId malformed")
	}
	return nil
}
