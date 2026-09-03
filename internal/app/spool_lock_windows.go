//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileExclusiveLock = 0x00000002
	lockfileFailImmediate = 0x00000001
	errorLockViolation    = syscall.Errno(33)
	errorNotLocked        = syscall.Errno(158)
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func acquireWebhookSpoolOwner(root *os.Root) (*os.File, error) {
	if root == nil {
		return nil, errors.New("webhook spool root is unavailable")
	}
	info, err := root.Lstat(webhookSpoolOwnerName)
	if err == nil {
		if windowsFileInfoIsReparsePoint(info) || !info.Mode().IsRegular() {
			return nil, errors.New("webhook spool owner path is unsafe")
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect webhook spool owner: %w", err)
	}
	file, err := root.OpenFile(webhookSpoolOwnerName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open webhook spool owner: %w", err)
	}
	info, err = file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat webhook spool owner: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("webhook spool owner path is unsafe")
	}
	if err := validateWebhookSpoolOwner(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("unsafe webhook spool owner: %w", err)
	}
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileEx.Call(
		uintptr(file.Fd()),
		lockfileExclusiveLock|lockfileFailImmediate,
		0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		_ = file.Close()
		if callErr == errorLockViolation {
			return nil, errors.New("webhook spool is already owned")
		}
		return nil, fmt.Errorf("lock webhook spool owner: %w", callErr)
	}
	return file, nil
}

func releaseWebhookSpoolOwner(file *os.File) error {
	if file == nil {
		return nil
	}
	var overlapped syscall.Overlapped
	_, _, unlockErr := unlockFileEx.Call(
		uintptr(file.Fd()), 0, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	closeErr := file.Close()
	if unlockErr != syscall.Errno(0) && unlockErr != errorNotLocked {
		return unlockErr
	}
	return closeErr
}
