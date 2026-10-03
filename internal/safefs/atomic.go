package safefs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadRegularFile refuses final symlinks, special files, and oversized input.
func ReadRegularFile(path string, maximum int64) ([]byte, error) {
	file, err := OpenReadNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximum)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximum)
	}
	return data, nil
}

// WriteFileAtomic writes a synced sibling and replaces the target only after
// the complete write succeeds. Existing permissions are preserved. Final
// symlinks and special files are refused; a hardlink is replaced, not modified.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file", path)
		}
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hooversion-write-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	defer file.Close()
	if err := file.Chmod(perm); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// RequireContainedPath refuses traversal and symlink components below a
// checkout, allowing missing descendants for generated release files. The
// checkout itself may use an OS alias such as macOS /var -> /private/var.
func RequireContainedPath(root, path string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, path)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes repository: %s", path)
	}
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		root = filepath.Join(root, part)
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("repository path contains a symbolic link: %s", path)
		}
	}
	return nil
}
