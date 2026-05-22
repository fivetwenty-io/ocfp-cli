package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMergePVEDefaults covers the four precedence cases for PVE credential inheritance.
func TestMergePVEDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		bloc     *Config
		defaults *PVEDefaults
		want     Config // expected state of bloc after merge
	}{
		{
			name: "bloc has all creds set defaults nil bloc fields unchanged",
			bloc: &Config{
				AuthToken:   "bloc-token",
				TokenSecret: "bloc-secret",
				Username:    "bloc-user",
				Password:    "bloc-pass",
			},
			defaults: nil,
			want: Config{
				AuthToken:   "bloc-token",
				TokenSecret: "bloc-secret",
				Username:    "bloc-user",
				Password:    "bloc-pass",
			},
		},
		{
			name: "bloc has no creds defaults has all four bloc fields populated from defaults",
			bloc: &Config{},
			defaults: &PVEDefaults{
				AuthToken:   "global-token",
				TokenSecret: "global-secret",
				Username:    "global-user",
				Password:    "global-pass",
			},
			want: Config{
				AuthToken:   "global-token",
				TokenSecret: "global-secret",
				Username:    "global-user",
				Password:    "global-pass",
			},
		},
		{
			name: "bloc has auth_token and token_secret defaults has all four bloc keeps its values inherits username and password",
			bloc: &Config{
				AuthToken:   "bloc-token",
				TokenSecret: "bloc-secret",
			},
			defaults: &PVEDefaults{
				AuthToken:   "global-token",
				TokenSecret: "global-secret",
				Username:    "global-user",
				Password:    "global-pass",
			},
			want: Config{
				AuthToken:   "bloc-token",
				TokenSecret: "bloc-secret",
				Username:    "global-user",
				Password:    "global-pass",
			},
		},
		{
			name: "bloc has no creds defaults nil all bloc fields remain empty no panic",
			bloc: &Config{},
			defaults: nil,
			want: Config{
				AuthToken:   "",
				TokenSecret: "",
				Username:    "",
				Password:    "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NotNil(t, tt.bloc, "test setup: bloc must not be nil")

			mergePVEDefaults(tt.bloc, tt.defaults)

			assert.Equal(t, tt.want.AuthToken, tt.bloc.AuthToken, "AuthToken mismatch")
			assert.Equal(t, tt.want.TokenSecret, tt.bloc.TokenSecret, "TokenSecret mismatch")
			assert.Equal(t, tt.want.Username, tt.bloc.Username, "Username mismatch")
			assert.Equal(t, tt.want.Password, tt.bloc.Password, "Password mismatch")
		})
	}
}

// TestMergePVEDefaultsNilBloc verifies the function is safe when bloc is nil.
func TestMergePVEDefaultsNilBloc(t *testing.T) {
	t.Parallel()

	defaults := &PVEDefaults{
		AuthToken:   "token",
		TokenSecret: "secret",
		Username:    "user",
		Password:    "pass",
	}

	// Must not panic.
	assert.NotPanics(t, func() {
		mergePVEDefaults(nil, defaults)
	})
}
