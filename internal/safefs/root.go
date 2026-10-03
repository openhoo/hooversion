package safefs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileSystem is the repository file seam shared by manifest mutations.
// Paths may be absolute or relative to the repository root.
type FileSystem interface {
	ReadRegularFile(string, int64) ([]byte, error)
	WriteFileAtomic(string, []byte, os.FileMode) error
}

// Root pins the checkout directory for the lifetime of a release. File
// operations cannot escape it even if another process swaps a parent directory.
type Root struct {
	root     *os.Root
	path     string
	identity os.FileInfo
}

func OpenRoot(path string) (*Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	return &Root{root: root, path: absolute, identity: info}, nil
}
func (r *Root) Path() string { return r.path }
func (r *Root) Close() error { return r.root.Close() }
func (r *Root) CheckIdentity() error {
	info, err := os.Stat(r.path)
	if err != nil {
		return err
	}
	if !os.SameFile(info, r.identity) {
		return fmt.Errorf("checkout directory identity changed: %s", r.path)
	}
	return nil
}
func (r *Root) relative(path string) (string, error) {
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(r.path, path)
		if err != nil {
			return "", err
		}
	}
	path = filepath.Clean(path)
	if !filepath.IsLocal(path) {
		return "", fmt.Errorf("path escapes repository: %s", path)
	}
	return path, nil
}
func (r *Root) Lstat(path string) (os.FileInfo, error) {
	p, e := r.relative(path)
	if e != nil {
		return nil, e
	}
	return r.root.Lstat(p)
}
func (r *Root) MkdirAll(path string, perm os.FileMode) error {
	p, e := r.relative(path)
	if e != nil {
		return e
	}
	return r.root.MkdirAll(p, perm)
}
func (r *Root) Remove(path string) error {
	p, e := r.relative(path)
	if e != nil {
		return e
	}
	return r.root.Remove(p)
}
func (r *Root) Readlink(path string) (string, error) {
	p, e := r.relative(path)
	if e != nil {
		return "", e
	}
	return r.root.Readlink(p)
}
func (r *Root) Symlink(target, path string) error {
	p, e := r.relative(path)
	if e != nil {
		return e
	}
	return r.root.Symlink(target, p)
}

// parent binds the mutation directory as well as the checkout. A rename
// racing with a directory swap stays in this directory, never in its replacement.
func (r *Root) parent(path string) (*os.Root, string, error) {
	p, e := r.relative(path)
	if e != nil {
		return nil, "", e
	}
	if p == "." {
		return nil, "", fmt.Errorf("expected file path")
	}
	// Reject existing symlinks; os.Root additionally enforces containment during races.
	current := ""
	for _, part := range strings.Split(filepath.Dir(p), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := r.root.Lstat(current)
		if err != nil {
			return nil, "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("repository path contains a symbolic link: %s", path)
		}
	}
	parent, e := r.root.OpenRoot(filepath.Dir(p))
	return parent, filepath.Base(p), e
}
func (r *Root) ReadRegularFile(path string, maximum int64) ([]byte, error) {
	parent, name, err := r.parent(path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	f, err := parent.OpenFile(name, readRootFlags(), 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file identity changed: %s", path)
	}
	if opened.Size() > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximum)
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximum)
	}
	return data, nil
}
func (r *Root) WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return r.writeFileAtomic(path, data, perm, true)
}

// RestoreFile restores the original permission bits as well as bytes.
func (r *Root) RestoreFile(path string, data []byte, perm os.FileMode) error {
	return r.writeFileAtomic(path, data, perm, false)
}
func (r *Root) writeFileAtomic(path string, data []byte, perm os.FileMode, preserve bool) error {
	parent, name, err := r.parent(path)
	if err != nil {
		return err
	}
	defer parent.Close()
	info, err := parent.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file", path)
		}
		if preserve {
			perm = info.Mode().Perm()
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	temporary := ".hooversion-write-" + hex.EncodeToString(nonce) + ".tmp"
	file, err := parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer parent.Remove(temporary)
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = parent.Rename(temporary, name); err != nil {
		return err
	}
	return syncDirectory(parent)
}
func (r *Root) Sync() error { return syncDirectory(r.root) }
func syncDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	err = file.Sync()
	// Some filesystems/platforms do not implement directory sync.
	if err != nil && !unsupportedDirectorySync(err) {
		return err
	}
	return nil
}

// Native preserves standalone APIs while release execution supplies a Root.
type Native struct{}

func (Native) ReadRegularFile(p string, n int64) ([]byte, error) { return ReadRegularFile(p, n) }
func (Native) WriteFileAtomic(p string, b []byte, m fs.FileMode) error {
	return WriteFileAtomic(p, b, m)
}
