package commands

import (
	"os"
	"strings"
	"testing"
)

// dnsChallengeFiles lists the files of the DNS challenge token, relative to
// this package's directory. Each must exist, so the list can't drift.
var dnsChallengeFiles = []string{
	"dnschallenge.go",
	"dnschallenge_test.go",
	"dnschallenge_name_test.go",
	"../cloudflare/token.go",
	"../cloudflare/token_egress_test.go",
	"../cloudflare/token_create_test.go",
	"../config/egress_ranges_test.go",
	"../../docs/config.pve.example.yml",
}

// TestDNSChallengeFilesNeverNameTheManagingSystem searches the files of
// the DNS challenge token for the managing system's name, which the open
// CLI never uses. The name is spelled from code points so that this file
// never holds it.
func TestDNSChallengeFilesNeverNameTheManagingSystem(t *testing.T) {
	t.Parallel()

	name := string([]rune{0x63, 0x6f, 0x73, 0x6d, 0x6f, 0x73})

	for _, path := range dnsChallengeFiles {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("reading %s: %v", path, err)

			continue
		}

		if strings.Contains(strings.ToLower(string(b)), name) {
			t.Errorf("%s holds the managing system's name", path)
		}
	}
}
