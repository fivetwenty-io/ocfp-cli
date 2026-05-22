package pve

import (
	"strings"
	"testing"
)

func TestTokenUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want string
	}{
		{"full header", "PVEAPIToken=root@pam!ocfp-bosh-cpi-root=8b452463-secret", "root@pam!ocfp-bosh-cpi-root"},
		{"missing prefix", "root@pam!foo=bar", "root@pam!foo"},
		{"no secret separator", "PVEAPIToken=root@pam!foo", "root@pam!foo"},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tokenUser(tc.in); got != tc.want {
				t.Errorf("tokenUser(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildVNCWebsocketURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, endpoint, node string
		vmid                 int
		port, ticket         string
		wantScheme           string
		wantContains         []string
	}{
		{
			name:         "https → wss",
			endpoint:     "https://pve.example.com:8006",
			node:         "pve",
			vmid:         101,
			port:         "5900",
			ticket:       "PVEVNC:abc",
			wantScheme:   "wss://",
			wantContains: []string{"/api2/json/nodes/pve/qemu/101/vncwebsocket", "port=5900", "vncticket=PVEVNC%3Aabc"},
		},
		{
			name:         "http → ws",
			endpoint:     "http://pve.local:8006",
			node:         "node1",
			vmid:         42,
			port:         "5901",
			ticket:       "x",
			wantScheme:   "ws://",
			wantContains: []string{"/api2/json/nodes/node1/qemu/42/vncwebsocket"},
		},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildVNCWebsocketURL(tc.endpoint, tc.node, tc.vmid, tc.port, tc.ticket)
			if err != nil {
				t.Fatalf("buildVNCWebsocketURL: %v", err)
			}

			if !strings.HasPrefix(got, tc.wantScheme) {
				t.Errorf("scheme: %q does not start with %q", got, tc.wantScheme)
			}

			for _, sub := range tc.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q in %q", sub, got)
				}
			}
		})
	}
}

func TestBuildVNCWebsocketURL_BadScheme(t *testing.T) {
	t.Parallel()

	_, err := buildVNCWebsocketURL("ftp://x", "pve", 1, "5900", "t")
	if err == nil {
		t.Error("expected error for unsupported scheme")
	}
}

func TestTail(t *testing.T) {
	t.Parallel()

	if got := tail("hello", 10); got != "hello" {
		t.Errorf("tail(<len): %q", got)
	}

	if got := tail("hello world", 5); got != "world" {
		t.Errorf("tail(>len): %q", got)
	}
}
