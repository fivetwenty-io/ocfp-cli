package output

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRenderer_AllModes(t *testing.T) {
	tests := []struct {
		name         string
		mode         Mode
		expectedType string
	}{
		{
			name:         "interactive mode",
			mode:         ModeInteractive,
			expectedType: "*output.InteractiveRenderer",
		},
		{
			name:         "concise mode",
			mode:         ModeConcise,
			expectedType: "*output.ConciseRenderer",
		},
		{
			name:         "json mode",
			mode:         ModeJSON,
			expectedType: "*output.JSONRenderer",
		},
		{
			name:         "yaml mode",
			mode:         ModeYAML,
			expectedType: "*output.YAMLRenderer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			r, err := NewRenderer(&buf, tt.mode)

			require.NoError(t, err)
			require.NotNil(t, r)

			// Verify we can use the renderer (basic smoke test)
			err = r.Close()
			assert.NoError(t, err)
		})
	}
}

func TestNewRenderer_InvalidMode(t *testing.T) {
	var buf bytes.Buffer
	invalidMode := Mode(999)

	r, err := NewRenderer(&buf, invalidMode)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMode)
	assert.Nil(t, r)
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected Mode
		wantErr  bool
	}{
		// Interactive mode
		{name: "interactive full", input: "interactive", expected: ModeInteractive, wantErr: false},
		{name: "interactive short", input: "i", expected: ModeInteractive, wantErr: false},
		{name: "interactive uppercase", input: "INTERACTIVE", expected: ModeInteractive, wantErr: false},
		{name: "interactive spaces", input: "  interactive  ", expected: ModeInteractive, wantErr: false},

		// Concise mode
		{name: "concise full", input: "concise", expected: ModeConcise, wantErr: false},
		{name: "concise short", input: "c", expected: ModeConcise, wantErr: false},
		{name: "concise uppercase", input: "CONCISE", expected: ModeConcise, wantErr: false},

		// JSON mode
		{name: "json full", input: "json", expected: ModeJSON, wantErr: false},
		{name: "json short", input: "j", expected: ModeJSON, wantErr: false},
		{name: "json uppercase", input: "JSON", expected: ModeJSON, wantErr: false},

		// YAML mode
		{name: "yaml full", input: "yaml", expected: ModeYAML, wantErr: false},
		{name: "yaml yml", input: "yml", expected: ModeYAML, wantErr: false},
		{name: "yaml short", input: "y", expected: ModeYAML, wantErr: false},
		{name: "yaml uppercase", input: "YAML", expected: ModeYAML, wantErr: false},

		// Invalid modes
		{name: "invalid mode", input: "invalid", wantErr: true},
		{name: "empty string", input: "", wantErr: true},
		{name: "random text", input: "foobar", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, err := ParseMode(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidMode)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, mode)
			}
		})
	}
}

func TestModeString(t *testing.T) {
	tests := []struct {
		mode     Mode
		expected string
	}{
		{mode: ModeInteractive, expected: "interactive"},
		{mode: ModeConcise, expected: "concise"},
		{mode: ModeJSON, expected: "json"},
		{mode: ModeYAML, expected: "yaml"},
		{mode: Mode(999), expected: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.mode.String())
		})
	}
}
