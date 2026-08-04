package bootstrap

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. Restores os.Stdout before returning even if fn
// panics.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()

	origStdout := os.Stdout

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	os.Stdout = w

	defer func() {
		os.Stdout = origStdout
	}()

	fn()

	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("close pipe writer: %v", closeErr)
	}

	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("read pipe: %v", copyErr)
	}

	return buf.Bytes()
}

func newTestManagerForPlan(blocName string) *Manager {
	return &Manager{
		options:  &Options{BlocName: blocName},
		metadata: NewMetadataManager(blocName),
	}
}

// TestAddKeypairSectionUsesResolvedSSHKeyDir asserts the dry-run plan
// table's "Storage" row shows the actual resolved SSH key directory
// (config.OcfpSSHKeyDir) rather than a hardcoded "~/.ocfp" literal.
func TestAddKeypairSectionUsesResolvedSSHKeyDir(t *testing.T) {
	const blocName = "testbloc"

	m := newTestManagerForPlan(blocName)
	plan := &bootstrapPlan{
		KeyPair: &keypairPreview{
			Name:    blocName + "-keypair",
			KeyType: "ed25519",
		},
	}

	table := ui.NewTable("")
	m.addKeypairSection(table, plan)

	wantDir := config.OcfpSSHKeyDir(blocName)

	var storageValue string

	found := false

	for _, section := range table.Sections {
		for _, row := range section.Rows {
			if len(row) == 2 && row[0] == "Storage" { //nolint:mnd
				storageValue = row[1]
				found = true
			}
		}
	}

	if !found {
		t.Fatalf("Storage row not found in keypair section, sections: %+v", table.Sections)
	}

	if !strings.Contains(storageValue, wantDir) {
		t.Errorf("Storage row = %q, want it to contain resolved SSH key dir %q", storageValue, wantDir)
	}

	if strings.Contains(storageValue, "~/.ocfp") {
		t.Errorf("Storage row = %q, must not contain hardcoded legacy literal %q", storageValue, "~/.ocfp")
	}
}

// TestShowPlannedKeypairUsesResolvedSSHKeyDir asserts the "Local Storage"
// row printed by showPlannedKeypair shows the actual resolved SSH key
// directory (config.OcfpSSHKeyDir) rather than a hardcoded "~/.ocfp"
// literal.
func TestShowPlannedKeypairUsesResolvedSSHKeyDir(t *testing.T) {
	const blocName = "testbloc"

	m := newTestManagerForPlan(blocName)
	plan := &bootstrapPlan{
		KeyPair: &keypairPreview{
			Name:    blocName + "-keypair",
			KeyType: "rsa",
		},
	}

	wantDir := config.OcfpSSHKeyDir(blocName)

	out := captureStdout(t, func() {
		m.showPlannedKeypair(plan)
	})

	output := string(out)

	if !strings.Contains(output, wantDir) {
		t.Errorf("showPlannedKeypair output = %q, want it to contain resolved SSH key dir %q", output, wantDir)
	}

	if strings.Contains(output, "~/.ocfp") {
		t.Errorf("showPlannedKeypair output = %q, must not contain hardcoded legacy literal %q", output, "~/.ocfp")
	}
}
