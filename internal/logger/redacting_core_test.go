package logger

import (
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestRedactingCore_ScrubsAllowListedFieldsOnly is the integration-style test
// requested alongside the unit tests for RedactSecrets: it builds a real
// zap.SugaredLogger backed by an observer core wrapped by newRedactingCore
// (the same wrapping Initialize performs on the production file core), logs
// a Debugw call whose "command" field contains a fake rendered
// `export GITHUB_TOKEN='...'` line, and asserts that:
//   - the allow-listed "command" field lands redacted in the entry actually
//     written by the wrapped core (proving the redaction happens at the
//     zapcore.Field layer, not just in a standalone RedactSecrets call), and
//   - a field with a non-allow-listed key ("host") passes through completely
//     untouched, even though its value also happens to contain a token-shaped
//     substring, proving the scrub is scoped to the allow-list and not a
//     blanket scan of every field.
func TestRedactingCore_ScrubsAllowListedFieldsOnly(t *testing.T) {
	t.Parallel()

	const fakeToken = "gho_FAKEFAKEFAKEFAKEFAKE1234" // #nosec -- test fixture, not a real credential

	observedCore, logs := observer.New(zapcore.DebugLevel)
	wrapped := newRedactingCore(observedCore)
	zapLogger := zap.New(wrapped)
	sugar := zapLogger.Sugar()

	fullScript := "#!/bin/bash\nexport GITHUB_TOKEN='" + fakeToken + "'\necho done\n"

	sugar.Debugw("Executing remote command",
		"command", fullScript,
		"host", "bastion-"+fakeToken, // non-allow-listed key; must NOT be touched
	)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 logged entry, got %d", len(entries))
	}

	fields := entries[0].ContextMap()

	commandVal, ok := fields["command"].(string)
	if !ok {
		t.Fatalf("expected string 'command' field, got %#v", fields["command"])
	}

	if strings.Contains(commandVal, fakeToken) {
		t.Fatalf("expected 'command' field to be redacted, got: %q", commandVal)
	}

	if !strings.Contains(commandVal, "export GITHUB_TOKEN='[REDACTED]'") {
		t.Fatalf("expected redacted export line in 'command' field, got: %q", commandVal)
	}

	if !strings.Contains(commandVal, "#!/bin/bash") || !strings.Contains(commandVal, "echo done") {
		t.Fatalf("expected surrounding script text to survive redaction, got: %q", commandVal)
	}

	hostVal, ok := fields["host"].(string)
	if !ok {
		t.Fatalf("expected string 'host' field, got %#v", fields["host"])
	}

	wantHost := "bastion-" + fakeToken
	if hostVal != wantHost {
		t.Fatalf("expected non-allow-listed 'host' field untouched, got %q, want %q", hostVal, wantHost)
	}
}

// TestRedactingCore_With verifies that fields attached via Sugar().With(...)
// (zap's mechanism for logger-scoped fields, e.g. bloc/request_id context in
// this codebase) are also redacted when they use an allow-listed key.
func TestRedactingCore_With(t *testing.T) {
	t.Parallel()

	const fakeToken = "ghp_FAKEFAKEFAKEFAKEFAKE5678" // #nosec -- test fixture, not a real credential

	observedCore, logs := observer.New(zapcore.DebugLevel)
	wrapped := newRedactingCore(observedCore)
	zapLogger := zap.New(wrapped)

	scoped := zapLogger.With(zap.String("script", "export GITHUB_TOKEN='"+fakeToken+"'"))
	scoped.Sugar().Debug("running script")

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 logged entry, got %d", len(entries))
	}

	fields := entries[0].ContextMap()

	scriptVal, ok := fields["script"].(string)
	if !ok {
		t.Fatalf("expected string 'script' field, got %#v", fields["script"])
	}

	if strings.Contains(scriptVal, fakeToken) {
		t.Fatalf("expected 'script' field attached via With() to be redacted, got: %q", scriptVal)
	}
}

// TestRedactingCore_ScrubsEscapedCommandShape reproduces, through the actual
// zapcore.Core wrapper (not just a standalone RedactSecrets call), the exact
// leak the adversarial review found: the "command" field logged in
// internal/bastion is the script after escapeShellString + "bash -c "
// wrapping, not the raw rendered script, so the secret's bounding `'` is
// folded into `'"'"'` by the time it reaches the log call. This asserts the
// wrapped core still redacts it end-to-end.
func TestRedactingCore_ScrubsEscapedCommandShape(t *testing.T) {
	t.Parallel()

	const fakeToken = "gho_FAKEFAKEFAKEFAKEFAKE4242" // #nosec -- test fixture, not a real credential

	observedCore, logs := observer.New(zapcore.DebugLevel)
	wrapped := newRedactingCore(observedCore)
	sugar := zap.New(wrapped).Sugar()

	script := "export GITHUB_TOKEN='" + fakeToken + "'\necho done\n"
	escaped := strings.ReplaceAll(script, "'", `'"'"'`) // mirrors escapeShellString in internal/bastion/phases.go
	cmd := "bash -c '" + escaped + "'"

	sugar.Debugw("Executing remote command", "command", cmd)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 logged entry, got %d", len(entries))
	}

	commandVal, ok := entries[0].ContextMap()["command"].(string)
	if !ok {
		t.Fatalf("expected string 'command' field, got %#v", entries[0].ContextMap()["command"])
	}

	if strings.Contains(commandVal, fakeToken) {
		t.Fatalf("expected escaped-command 'command' field to be redacted through the wrapped core, got: %q", commandVal)
	}

	if !strings.Contains(commandVal, "[REDACTED]") {
		t.Fatalf("expected a [REDACTED] marker in the escaped-command field, got: %q", commandVal)
	}
}
