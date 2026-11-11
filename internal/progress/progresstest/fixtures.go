// Package progresstest holds golden progress streams for tests.
package progresstest

import (
	"embed"
	"fmt"
)

//go:embed testdata
var files embed.FS

// Fixture returns the bytes of the named golden file. It panics when the name
// is unknown, because a test that names a missing fixture is a programming
// error.
func Fixture(name string) []byte {
	b, err := files.ReadFile("testdata/" + name)
	if err != nil {
		panic(fmt.Sprintf("progresstest: unknown fixture %q", name))
	}

	return b
}
