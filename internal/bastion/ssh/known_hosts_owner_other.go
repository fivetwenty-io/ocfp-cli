//go:build !unix

package ssh

import "os"

// keepOwner does nothing where files carry no Unix owner.
func keepOwner(_ *os.File, _ os.FileInfo) {}
