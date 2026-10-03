//go:build !windows

package safefs

import (
	"os"
	"syscall"
)

func lockOpenFlags() int { return os.O_CREATE | os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK }
func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func unlockExclusive(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
func readRootFlags() int               { return os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW }
