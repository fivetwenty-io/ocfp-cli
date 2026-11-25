//go:build !unix

package enroll

import (
	"fmt"
	"os"
)

// OpenNoFollow refuses on a system without the file rules' calls. The
// enrollment service and its root steps run only on a unix bastion.
func OpenNoFollow(dir, name string, _ int64) (*os.File, int64, error) {
	return nil, 0, fmt.Errorf("%w: %s/%s: the file rules need a unix system", ErrFileRule, dir, name)
}

// ReadNoFollow refuses on a system without the file rules' calls.
func ReadNoFollow(dir, name string, _ int64) ([]byte, error) {
	return nil, fmt.Errorf("%w: %s/%s: the file rules need a unix system", ErrFileRule, dir, name)
}

// CreateExclusive refuses on a system without the file rules' calls.
func CreateExclusive(dir, name string, _ []byte, _, _ int, _ os.FileMode) error {
	return fmt.Errorf("%w: %s/%s: the file rules need a unix system", ErrFileRule, dir, name)
}
