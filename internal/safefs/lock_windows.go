//go:build windows

package safefs

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var lockKernel = syscall.NewLazyDLL("kernel32.dll")
var lockProc = lockKernel.NewProc("LockFileEx")
var unlockProc = lockKernel.NewProc("UnlockFileEx")

func lockOpenFlags() int { return os.O_CREATE | os.O_RDWR }
func readRootFlags() int { return os.O_RDONLY }
func lockExclusive(f *os.File) error {
	var o syscall.Overlapped
	r, _, e := lockProc.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&o)))
	if r == 0 {
		return fmt.Errorf("lock: %w", e)
	}
	return nil
}
func unlockExclusive(f *os.File) error {
	var o syscall.Overlapped
	r, _, e := unlockProc.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&o)))
	if r == 0 {
		return fmt.Errorf("unlock: %w", e)
	}
	return nil
}
