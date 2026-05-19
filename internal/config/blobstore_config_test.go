package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestBlobstoreConfigUnmarshalAliases covers both snake_case and camelCase
// keys so operators can pick either style. Confused aliases (mixing
// access_key + accessKey in one document) take the snake_case value to match
// the README contract.
func TestBlobstoreConfigUnmarshalAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		wantAK  string
		wantSK  string
		wantPS  bool
		wantReg string
	}{
		{
			name: "snake_case",
			yaml: `mode: external
endpoint: https://s3.example.com
region: eu-west-1
access_key: AKIA-1
secret_key: secret-1
path_style: true`,
			wantAK:  "AKIA-1",
			wantSK:  "secret-1",
			wantPS:  true,
			wantReg: "eu-west-1",
		},
		{
			name: "camelCase",
			yaml: `mode: external
endpoint: https://s3.example.com
region: eu-west-1
accessKey: AKIA-2
secretKey: secret-2
pathStyle: false`,
			wantAK:  "AKIA-2",
			wantSK:  "secret-2",
			wantPS:  false,
			wantReg: "eu-west-1",
		},
		{
			name: "snake_case wins when both are present",
			yaml: `mode: external
access_key: snake-key
accessKey: camel-key
secret_key: snake-secret
secretKey: camel-secret
endpoint: https://s3.example.com`,
			wantAK: "snake-key",
			wantSK: "snake-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b BlobstoreConfig

			err := yaml.Unmarshal([]byte(tt.yaml), &b)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if b.AccessKey != tt.wantAK {
				t.Errorf("AccessKey = %q, want %q", b.AccessKey, tt.wantAK)
			}

			if b.SecretKey != tt.wantSK {
				t.Errorf("SecretKey = %q, want %q", b.SecretKey, tt.wantSK)
			}

			if tt.wantReg != "" && b.Region != tt.wantReg {
				t.Errorf("Region = %q, want %q", b.Region, tt.wantReg)
			}

			if tt.name == "snake_case" || tt.name == "camelCase" {
				if b.ResolvedPathStyle() != tt.wantPS {
					t.Errorf("PathStyle resolved = %v, want %v", b.ResolvedPathStyle(), tt.wantPS)
				}
			}
		})
	}
}

// TestBlobstoreConfigResolvedMode covers the local-default behaviour. Empty
// mode must resolve to "local" so bootstrap step 7 skips bucket creation.
func TestBlobstoreConfigResolvedMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "empty defaults to local", mode: "", want: "local"},
		{name: "explicit local", mode: "local", want: "local"},
		{name: "explicit external", mode: "external", want: "external"},
		{name: "external mixed case via Unmarshal", mode: "External", want: "external"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			yamlText := "mode: " + tt.mode
			if tt.mode == "" {
				yamlText = "{}"
			}

			var b BlobstoreConfig
			if err := yaml.Unmarshal([]byte(yamlText), &b); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if got := b.ResolvedMode(); got != tt.want {
				t.Errorf("ResolvedMode() = %q, want %q (mode=%q)", got, tt.want, tt.mode)
			}
		})
	}
}

// TestBlobstoreConfigValidateExternal asserts Validate flags missing endpoint
// + credentials in external mode. Local mode validates clean even with empty
// fields, which is what makes the default config (no blobstore section)
// usable as-is.
func TestBlobstoreConfigValidateExternal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   BlobstoreConfig
		wantErr error
	}{
		{
			name:    "local mode passes with no fields",
			input:   BlobstoreConfig{Mode: ""},
			wantErr: nil,
		},
		{
			name: "external without endpoint",
			input: BlobstoreConfig{
				Mode:      "external",
				AccessKey: "k",
				SecretKey: "s",
			},
			wantErr: ErrBlobstoreEndpointRequired,
		},
		{
			name: "external without credentials",
			input: BlobstoreConfig{
				Mode:     "external",
				Endpoint: "https://s3.example.com",
			},
			wantErr: ErrBlobstoreCredentialsRequired,
		},
		{
			name: "external fully populated",
			input: BlobstoreConfig{
				Mode:      "external",
				Endpoint:  "https://s3.example.com",
				AccessKey: "k",
				SecretKey: "s",
			},
			wantErr: nil,
		},
		{
			name: "unknown mode",
			input: BlobstoreConfig{
				Mode: "weird",
			},
			wantErr: ErrBlobstoreInvalidMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.input.Validate()

			switch {
			case tt.wantErr == nil && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}

			// Sanity: any error message must mention the relevant field
			// so operators can find it.
			if err != nil && !strings.Contains(err.Error(), "blobstore") {
				t.Errorf("error %q does not mention 'blobstore'", err)
			}
		})
	}
}
