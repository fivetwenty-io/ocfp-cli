// Package ownerrecord defines the owner record, a controller lock that names
// the one managing system allowed to act on a bloc, and its signed form.
//
// The record lives on the bloc's bastion. A record is trusted only after Open
// has checked its Ed25519 signature against the owner public key it carries,
// over the signing context followed by the exact payload bytes.
package ownerrecord

import (
	"regexp"
	"time"
)

// NamePattern is the form of a job id and an org, either of which may become
// part of a path on the host. It can never hold a path separator or start with
// a dot or a dash.
var NamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// BlocPattern is the form of every bloc name: lower-case letters, digits, and
// dashes, at least two characters, starting and ending with a letter or a
// digit. It is the rule init applies when it creates a bloc, so every bloc the
// tool makes is one an owner record can name. It can never hold a path
// separator or start with a dot or a dash.
var BlocPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// Mode says whether the owner may act on the bloc or is handing it over.
type Mode string

// The owner record modes.
const (
	// ModeLive means the named owner may act on the bloc.
	ModeLive Mode = "live"

	// ModeHandover means ownership is moving to another instance, and no job
	// may start until a later record names the new owner as live.
	ModeHandover Mode = "handover"
)

// Record is the payload of an owner record.
type Record struct {
	// InstallationID names the installation that owns the bloc.
	InstallationID string `json:"installationId"`

	// Org and Bloc name the organisation and the bloc that this record's host
	// serves. A job request must name the same two. Org matches NamePattern
	// and Bloc matches BlocPattern.
	Org  string `json:"org"`
	Bloc string `json:"bloc"`

	// OwnerInstanceID names the instance of that installation that may act.
	OwnerInstanceID string `json:"ownerInstanceId"`

	// OwnerPublicKey is the owner instance's Ed25519 public key. Open verifies
	// the record's signature with it, and the job wrapper verifies job
	// requests with it.
	OwnerPublicKey []byte `json:"ownerPublicKey"`

	// Epoch is the installation's ownership epoch.
	Epoch uint64 `json:"epoch"`

	// Mode is ModeLive or ModeHandover.
	Mode Mode `json:"mode"`

	// Generation rises with every write of the record, so a reader that
	// remembers the last generation it wrote can spot a record restored from
	// an older copy.
	Generation uint64 `json:"generation"`

	// HeadPosition and HeadHash name the owner's history head when the record
	// was written.
	HeadPosition int64  `json:"headPosition"`
	HeadHash     string `json:"headHash"`

	// Relocation describes a move between instances. It is required in
	// handover and may remain once the new owner is live.
	Relocation *Relocation `json:"relocation,omitempty"`

	// SignerID names the instance that wrote and signed the record.
	SignerID string `json:"signerId"`

	// SignedAt is when the record was signed.
	SignedAt time.Time `json:"signedAt"`
}

// Relocation describes a move of ownership from one instance to another.
type Relocation struct {
	// ID names the relocation.
	ID string `json:"id"`

	// TargetInstanceID names the instance that ownership moves to.
	TargetInstanceID string `json:"targetInstanceId,omitempty"`

	// CutOverPosition and CutOverHash name the history entry at which the
	// move was cut over. They are set together, in the records written after
	// cutover, or not at all. The position is a pointer so that a cut-over at
	// position 0 is kept.
	CutOverPosition *int64 `json:"cutOverPosition,omitempty"`
	CutOverHash     string `json:"cutOverHash,omitempty"`
}
