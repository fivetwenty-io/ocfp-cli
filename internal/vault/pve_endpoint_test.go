package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPVEHostnameOnly(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "bare host", in: "pve.example.com", want: "pve.example.com"},
		{name: "bare host with port", in: "pve.example.com:8006", want: "pve.example.com"},
		{name: "bare IPv4", in: "10.254.16.5", want: "10.254.16.5"},
		{name: "bare IPv4 with port", in: "10.254.16.5:8006", want: "10.254.16.5"},
		{name: "bare unbracketed IPv6", in: "fd00::1", want: "fd00::1"},
		{name: "bare bracketed IPv6 with port", in: "[fd00::1]:8006", want: "[fd00::1]"},
		{name: "URL with port", in: "https://pve.example.com:8006", want: "pve.example.com"},
		{name: "URL without port", in: "https://pve.example.com", want: "pve.example.com"},
		{name: "URL with trailing slash", in: "https://host:8006/", want: "host"},
		{name: "URL with path", in: "https://host:8006/api2/json", want: "host"},
		{name: "URL with query", in: "https://host:8006?x=1", want: "host"},
		{name: "URL with path and query", in: "https://host/api?x=1", want: "host"},
		{name: "URL with fragment", in: "https://host:8006#frag", want: "host"},
		{name: "URL with user info", in: "https://u:p@host:8006", want: "host"},
		{name: "URL with user only", in: "https://u@host", want: "host"},
		{name: "URL with user info and path", in: "https://u:p@host:8006/api", want: "host"},
		{name: "bracketed IPv6 URL with port", in: "https://[fd00::1]:8006", want: "[fd00::1]"},
		{name: "bracketed IPv6 URL with path", in: "https://[::1]:8006/api", want: "[::1]"},
		{name: "bracketed IPv6 URL without port", in: "https://[::1]", want: "[::1]"},
		{name: "unbracketed IPv6 URL", in: "https://fd00::1", want: "fd00::1"},
		{name: "unbracketed IPv6 URL with path", in: "https://fd00::1/api", want: "fd00::1"},
		{name: "unbracketed IPv6 with user info", in: "https://u:p@fd00::1", want: "fd00::1"},
		{name: "http scheme", in: "http://host:80", want: "host"},
		{name: "surrounding whitespace", in: "  https://host:8006  ", want: "host"},
		{name: "scheme only", in: "https://", want: ""},
		{name: "port only", in: ":8006", want: ""},
		{name: "empty", in: "", want: ""},
		{name: "non-numeric port is invalid", in: "https://host:abc", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pveHostnameOnly(tt.in))
		})
	}
}

// TestPVEEndpointPortShapes runs pveEndpointPort over every endpoint shape
// the PVE config accepts.
func TestPVEEndpointPortShapes(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{name: "bare host defaults", in: "pve.example.com", want: 8006},
		{name: "bare host with port", in: "pve.example.com:8443", want: 8443},
		{name: "bare IPv4 defaults", in: "10.254.16.5", want: 8006},
		{name: "bare IPv4 with port", in: "10.254.16.5:8443", want: 8443},
		{name: "bare unbracketed IPv6 is not a port", in: "fd00::1", want: 8006},
		{name: "bare bracketed IPv6 with port", in: "[fd00::1]:8443", want: 8443},
		{name: "URL with port", in: "https://pve.example.com:8443", want: 8443},
		{name: "URL without port", in: "https://pve.example.com", want: 8006},
		{name: "URL with trailing slash", in: "https://host:8443/", want: 8443},
		{name: "URL with path", in: "https://host:8443/api2/json", want: 8443},
		{name: "URL with query", in: "https://host:8443?x=1", want: 8443},
		{name: "URL with path and query but no port", in: "https://host/api?x=1:9999", want: 8006},
		{name: "URL with user info and port", in: "https://u:p@host:8443", want: 8443},
		{name: "URL with user info and no port", in: "https://u:p@host", want: 8006},
		{name: "bracketed IPv6 URL with port", in: "https://[::1]:8443/api", want: 8443},
		{name: "bracketed IPv6 URL without port", in: "https://[::1]", want: 8006},
		{name: "unbracketed IPv6 URL is not a port", in: "https://fd00::1", want: 8006},
		{name: "unbracketed IPv6 URL with path", in: "https://fd00::1/api", want: 8006},
		{name: "unbracketed IPv6 with user info", in: "https://u:p@fd00::1", want: 8006},
		{name: "zero port is an error", in: "https://host:0", wantErr: true},
		{name: "out of range port is an error", in: "https://host:70000", wantErr: true},
		{name: "non-numeric port is an error", in: "https://host:abc", wantErr: true},
		{name: "empty defaults", in: "", want: 8006},
		{name: "scheme only defaults", in: "https://", want: 8006},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pveEndpointPort(tt.in)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPVEEndpointPort(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{name: "missing port defaults", in: "https://pve.example.com", want: 8006},
		{name: "bare host defaults", in: "pve.example.com", want: 8006},
		{name: "empty port after colon defaults", in: "https://pve.example.com:/", want: 8006},
		{name: "unbracketed IPv6 has no port", in: "fd00::1", want: 8006},
		{name: "explicit port", in: "https://pve.example.com:8443/api", want: 8443},
		{name: "lowest port", in: "pve:1", want: 1},
		{name: "highest port", in: "pve:65535", want: 65535},
		{name: "port zero", in: "https://host:0", wantErr: true},
		{name: "port above range", in: "https://host:65536", wantErr: true},
		{name: "port far above range", in: "https://host:99999", wantErr: true},
		{name: "non-numeric port", in: "https://host:abc", wantErr: true},
		{name: "port with trailing letter", in: "host:8006x", wantErr: true},
		{name: "unparsable endpoint", in: "https://my host:8006", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pveEndpointPort(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.in, "the error names the endpoint")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
