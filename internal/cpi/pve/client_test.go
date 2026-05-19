package pve

import "testing"

// TestSplitPVEEndpoint covers the parser that lets operators write the PVE
// endpoint as a bare host, host:port, or full URL — apiclient's Options
// fields require them split, and the wrong form caused URL doubling
// (https://https//host:8006:8006/...) in earlier versions.
func TestSplitPVEEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		endpoint     string
		wantHost     string
		wantPort     int
		wantProtocol string
	}{
		{
			name:         "bare hostname",
			endpoint:     "pve.example.com",
			wantHost:     "pve.example.com",
			wantPort:     0,
			wantProtocol: "",
		},
		{
			name:         "host with port",
			endpoint:     "pve.example.com:8006",
			wantHost:     "pve.example.com",
			wantPort:     8006,
			wantProtocol: "",
		},
		{
			name:         "https url with port",
			endpoint:     "https://pve.example.com:8006",
			wantHost:     "pve.example.com",
			wantPort:     8006,
			wantProtocol: "https",
		},
		{
			name:         "http url with port",
			endpoint:     "http://pve.example.com:8006",
			wantHost:     "pve.example.com",
			wantPort:     8006,
			wantProtocol: "http",
		},
		{
			name:         "https url no port",
			endpoint:     "https://pve.example.com",
			wantHost:     "pve.example.com",
			wantPort:     0,
			wantProtocol: "https",
		},
		{
			name:         "url with trailing path is discarded",
			endpoint:     "https://pve.example.com:8006/api2/json",
			wantHost:     "pve.example.com",
			wantPort:     8006,
			wantProtocol: "https",
		},
		{
			name:         "tailscale dns name",
			endpoint:     "pve-0.taile80fe.ts.net",
			wantHost:     "pve-0.taile80fe.ts.net",
			wantPort:     0,
			wantProtocol: "",
		},
		{
			name:         "ipv4 host with port",
			endpoint:     "10.0.0.1:8006",
			wantHost:     "10.0.0.1",
			wantPort:     8006,
			wantProtocol: "",
		},
		{
			name:         "leading and trailing whitespace",
			endpoint:     "  https://pve.example.com:8006  ",
			wantHost:     "pve.example.com",
			wantPort:     8006,
			wantProtocol: "https",
		},
		{
			name:         "empty string",
			endpoint:     "",
			wantHost:     "",
			wantPort:     0,
			wantProtocol: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotHost, gotPort, gotProtocol := splitPVEEndpoint(tt.endpoint)

			if gotHost != tt.wantHost {
				t.Errorf("host = %q, want %q", gotHost, tt.wantHost)
			}

			if gotPort != tt.wantPort {
				t.Errorf("port = %d, want %d", gotPort, tt.wantPort)
			}

			if gotProtocol != tt.wantProtocol {
				t.Errorf("protocol = %q, want %q", gotProtocol, tt.wantProtocol)
			}
		})
	}
}
