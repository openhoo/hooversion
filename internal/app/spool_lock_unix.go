//go:build !windows

package app

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireWebhookSpoolOwner(root *os.Root) (*os.File, error) {
	if root == nil {
		return nil, errors.New("webhook spool root is unavailable")
	}
	info, err := root.Lstat(webhookSpoolOwnerName)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("webhook spool owner path is unsafe")
		}
		if err := validateWebhookSpoolOwner(info); err != nil {
			return nil, fmt.Errorf("unsafe webhook spool owner: %w", err)
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
	if err := validateWebhookSpoolOwner(info); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("unsafe webhook spool owner: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("webhook spool is already owned: %w", err)
	}
	return file, nil
}

func releaseWebhookSpoolOwner(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
