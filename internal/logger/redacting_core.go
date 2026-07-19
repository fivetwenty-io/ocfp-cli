package logger

import "go.uber.org/zap/zapcore"

// redactAllowListKeys is the fixed set of structured-log field keys that are
// known, by convention in this codebase, to carry rendered shell/script text
// (executed commands, their stdout/stderr, or a generated script body) and
// therefore might carry a literal secret value (e.g. a rendered
// `export GITHUB_TOKEN='<value>'` line). Only fields whose key is in this
// set are scrubbed - this bounds the regex cost to the log calls that can
// plausibly leak a secret instead of scanning every field on every call.
var redactAllowListKeys = map[string]bool{ //nolint:gochecknoglobals // fixed, read-only allow-list
	"command": true,
	"script":  true,
	"stdout":  true,
	"stderr":  true,
	"cmd":     true,
}

// newRedactingCore wraps a zapcore.Core so that, for every log entry written
// through it, any string-valued field whose key is in redactAllowListKeys
// has RedactSecrets applied to its value before the entry reaches the
// wrapped core (and therefore before it is encoded/written to disk).
//
// This is a structural fix: it also protects any *future* log call that
// reuses one of the allow-listed field keys (the `export KEY='value'`
// script-rendering pattern already exists in two independent places in this
// codebase - internal/bastion/phases.go and
// internal/bastion/provision/environment.go - and is not, on its own,
// audited for secrets at the render site).
//
//nolint:ireturn // returning zapcore.Core interface is intentional (zap API); mirrors createFileCore.
func newRedactingCore(core zapcore.Core) zapcore.Core {
	return &redactingCore{Core: core}
}

// redactingCore embeds zapcore.Core so it satisfies the interface via
// promotion for every method it does not explicitly override (Enabled,
// Sync). It overrides only With and Write, the two methods that see field
// values.
type redactingCore struct {
	zapcore.Core
}

// With returns a new core carrying the given fields (already redacted)
// alongside any fields the wrapped core already carries. zap calls this to
// build logger-scoped fields (e.g. `bloc`, `request_id`) via With(...).
func (c *redactingCore) With(fields []zapcore.Field) zapcore.Core {
	return &redactingCore{Core: c.Core.With(redactFields(fields))}
}

// Check lets the wrapped core decide whether the entry is enabled, but
// arranges for THIS core's Write to be the one zap calls, so field
// redaction always runs before the entry is serialized.
func (c *redactingCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Core.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}

	return checked
}

// Write redacts allow-listed fields, then delegates to the wrapped core.
func (c *redactingCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	return c.Core.Write(entry, redactFields(fields)) //nolint:wrapcheck // pass-through to underlying zapcore.Core, matches zap's own Core contract
}

// redactFields returns a copy of fields with RedactSecrets applied to the
// String value of any field whose Key is in redactAllowListKeys and whose
// Type is zapcore.StringType (the type zap.Any/Sugared loggers produce for a
// plain Go string argument, which is how every allow-listed field is logged
// in this codebase today). Fields outside the allow-list, or allow-listed
// keys carrying a non-string value, pass through unchanged.
func redactFields(fields []zapcore.Field) []zapcore.Field {
	out := make([]zapcore.Field, len(fields))

	for i, f := range fields {
		if f.Type == zapcore.StringType && redactAllowListKeys[f.Key] {
			f.String = RedactSecrets(f.String)
		}

		out[i] = f
	}

	return out
}
