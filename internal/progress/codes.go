package progress

import "errors"

// Code is a stable, machine-readable name for a refusal or failure.
type Code string

// The refusal codes. A code is never renamed once it ships.
const (
	// CodeOwnerRecordMissing reports that the host has no owner record.
	CodeOwnerRecordMissing Code = "OWNER_RECORD_MISSING"

	// CodeOwnerMismatch reports a record that names another installation or
	// epoch.
	CodeOwnerMismatch Code = "OWNER_MISMATCH"

	// CodeRequestUnsigned reports a request that the owner's instance key did
	// not sign.
	CodeRequestUnsigned Code = "REQUEST_UNSIGNED"

	// CodeOwnerHandover reports an owner record that is in handover.
	CodeOwnerHandover Code = "OWNER_HANDOVER"

	// CodeJobRequestInvalid reports a request that does not parse or whose
	// issue time falls outside the accepted window.
	CodeJobRequestInvalid Code = "JOB_REQUEST_INVALID"

	// CodeVerbNotAllowed reports a verb that the wrapper does not accept.
	CodeVerbNotAllowed Code = "VERB_NOT_ALLOWED"

	// CodeWireContractUnsupported reports a wire contract version that the
	// CLI cannot write.
	CodeWireContractUnsupported Code = "WIRE_CONTRACT_UNSUPPORTED"

	// CodeOwnerRecordExists reports a create-only write of an owner record
	// where one already exists.
	CodeOwnerRecordExists Code = "OWNER_RECORD_EXISTS"

	// CodeJobOrphaned reports an attach to a spool whose wrapper died without
	// writing an exit record.
	CodeJobOrphaned Code = "JOB_ORPHANED"
)

// Refusal is an error that carries a Code, so a caller can act on the reason
// without matching message text.
type Refusal struct { //nolint:errname // callers match on this name through AsRefusal
	Code    Code
	Message string
}

// Error returns the code followed by the message.
func (r *Refusal) Error() string {
	return string(r.Code) + ": " + r.Message
}

// AsRefusal finds a *Refusal anywhere in err's chain. A typed-nil *Refusal in
// the chain is not a refusal, so it answers false.
func AsRefusal(err error) (*Refusal, bool) {
	var r *Refusal
	if errors.As(err, &r) && r != nil {
		return r, true
	}

	return nil, false
}
