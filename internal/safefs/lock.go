package safefs

import (
	"fmt"
	"os"
	"path/filepath"
)

// Lock is cooperative process ownership. The persistent file is deliberately
// never unlinked: unlinking a locked inode would permit two different owners.
type Lock struct {
	file *os.File
	root *os.Root
}

func AcquireLock(path string) (*Lock, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err == nil && !info.Mode().IsRegular() {
		root.Close()
		return nil, fmt.Errorf("unsafe release lock: %s", path)
	}
	if err != nil && !os.IsNotExist(err) {
		root.Close()
		return nil, err
	}
	file, err := root.OpenFile(name, lockOpenFlags(), 0600)
	if err != nil {
		root.Close()
		return nil, err
	}
	opened, err := file.Stat()
	if err == nil && (!opened.Mode().IsRegular() || (info != nil && !os.SameFile(info, opened))) {
		err = fmt.Errorf("release lock identity changed")
	}
	if err == nil {
		err = lockExclusive(file)
	}
	if err != nil {
		file.Close()
		root.Close()
		return nil, fmt.Errorf("repository release is already owned or lock is unsafe: %w", err)
	}
	return &Lock{file: file, root: root}, nil
}
func (l *Lock) Close() error {
	unlockErr := unlockExclusive(l.file)
	closeErr := l.file.Close()
	l.root.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
