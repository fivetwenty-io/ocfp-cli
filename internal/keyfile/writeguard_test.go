package keyfile

import (
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
	"github.com/stretchr/testify/assert"
)

// Both writers take a path from their caller, so no resolver checks it
// first. The check in writeFile is the only thing between a caller's path
// and a real ocfp directory.
func TestWritersPanicForAPathUnderTheRealDirectories(t *testing.T) {
	for name, write := range map[string]func(string, string) error{
		"WriteNew":       WriteNew,
		"WriteRecovered": WriteRecovered,
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "keys")
			path := filepath.Join(dir, "id_ed25519")
			testenv.GuardOnly(t, path)

			assert.Contains(t, testenv.PanicMessage(func() { _ = write(path, "key") }), "keyfile write")
			assert.NoDirExists(t, dir, "the guard must stop the write before it creates anything")
		})
	}
}
