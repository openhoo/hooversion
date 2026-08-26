//go:build !windows

package safefs

import (
	"os"
	"syscall"
)

func openNoFollow(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func createExclusive(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
}
