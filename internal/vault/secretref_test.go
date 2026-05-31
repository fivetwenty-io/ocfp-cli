package vault

import "testing"

func TestResolveSecretRef(t *testing.T) {
	t.Parallel()

	m := newMockFullSafe()
	_ = m.Set("secret/config/x/cloudflare", "api_token", "tok-123")

	tests := []struct {
		name    string
		literal string
		ref     string
		nilSafe bool
		want    string
	}{
		{name: "literal wins over ref", literal: "lit-tok", ref: "secret/config/x/cloudflare:api_token", want: "lit-tok"},
		{name: "vault path resolves", ref: "secret/config/x/cloudflare:api_token", want: "tok-123"},
		{name: "empty inputs", want: ""},
		{name: "malformed ref no colon", ref: "secret/config/x/cloudflare", want: ""},
		{name: "trailing colon", ref: "secret/x:", want: ""},
		{name: "nil safe with valid ref", ref: "secret/x:k", nilSafe: true, want: ""},
		{name: "missing key returns empty", ref: "secret/config/x/cloudflare:absent", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var safe SafeInterface = m
			if tt.nilSafe {
				safe = nil
			}

			got := ResolveSecretRef(safe, tt.literal, tt.ref)
			if got != tt.want {
				t.Fatalf("ResolveSecretRef(%q, %q) = %q, want %q", tt.literal, tt.ref, got, tt.want)
			}
		})
	}
}
