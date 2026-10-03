//go:build !windows

package safefs

import (
	"errors"
	"syscall"
)

func unsupportedDirectorySync(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP)
}
