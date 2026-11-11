package progress

// Code is a stable, machine-readable name for a refusal or failure.
type Code string

// CodeOwnerRecordMissing reports that the host has no owner record.
const CodeOwnerRecordMissing Code = "OWNER_RECORD_MISSING"
