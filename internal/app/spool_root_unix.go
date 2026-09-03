//go:build unix

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validateWebhookSpoolOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) {
		return errors.New("webhook spool directory has unsafe owner")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("webhook spool directory has unsafe permissions")
	}
	return nil
}
func validateWebhookSpoolParent(info os.FileInfo) error {
	if info.Mode().Perm()&0o022 == 0 {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && stat.Uid == 0 && info.Mode()&os.ModeSticky != 0 && info.Mode().Perm()&0o002 != 0 {
		return nil
	}
	return errors.New("webhook spool parent has unsafe permissions")
}
func validateWebhookSpoolIntermediate(info os.FileInfo) error {
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("webhook spool path must be a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("webhook spool path has unavailable ownership")
	}
	if uint32(stat.Uid) == uint32(os.Geteuid()) {
		return validateWebhookSpoolParent(info)
	}
	if stat.Uid == 0 && info.Mode().Perm()&0o022 == 0 {
		return nil
	}
	if stat.Uid == 0 && info.Mode()&os.ModeSticky != 0 &&
		info.Mode().Perm()&0o002 != 0 {
		return nil
	}
	return errors.New("webhook spool path has unsafe intermediate owner")
}

func prepareWebhookSpoolDir(path string) error {
	if err := validateWebhookSpoolPathComponents(path); err != nil {
		return err
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return fmt.Errorf("open webhook spool filesystem root: %w", err)
	}
	defer root.Close()
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" {
		return errors.New("webhook spool path must not be filesystem root")
	}

	current := root
	defer func() {
		if current != root {
			_ = current.Close()
		}
	}()
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("webhook spool path contains unsafe component")
		}

		// Mkdir is the only operation that creates a component. Because part
		// is the final component of this descriptor-relative operation, mkdir
		// never follows an existing symlink.
		mkdirErr := current.Mkdir(part, 0o700)
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return fmt.Errorf("create webhook spool directory component: %w", mkdirErr)
		}
		info, statErr := current.Lstat(part)
		if statErr != nil {
			return fmt.Errorf("inspect webhook spool directory component: %w", statErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("webhook spool path must be a directory")
		}
		if index == len(parts)-1 {
			if err := validateWebhookSpoolOwner(info); err != nil {
				return err
			}
			return nil
		}
		if err := validateWebhookSpoolIntermediate(info); err != nil {
			return err
		}

		next, openErr := current.OpenRoot(part)
		if openErr != nil {
			return fmt.Errorf("open webhook spool directory component: %w", openErr)
		}
		openedInfo, openedErr := next.Stat(".")
		pathInfo, pathErr := current.Lstat(part)
		if openedErr != nil || pathErr != nil ||
			(pathInfo != nil && pathInfo.Mode()&os.ModeSymlink != 0) ||
			openedErr == nil && pathErr == nil && !os.SameFile(openedInfo, pathInfo) {
			_ = next.Close()
			if openedErr != nil {
				return fmt.Errorf("inspect opened webhook spool directory component: %w", openedErr)
			}
			if pathErr != nil {
				return fmt.Errorf("reinspect webhook spool directory component: %w", pathErr)
			}
			return errors.New("webhook spool path component changed during inspection")
		}
		if current != root {
			_ = current.Close()
		}
		current = next
	}
	return errors.New("webhook spool path must not be filesystem root")
}

func validateWebhookSpoolPathComponents(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("webhook spool path contains a symlink")
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect webhook spool path: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}
