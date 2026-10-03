//go:build windows

package safefs

import (
	"errors"
	"os"
	"syscall"
)

var errSymlinkRefused = errors.New("refusing to operate on a symbolic link")

func openNoFollow(path string, flag int) (*os.File, error) {
	encoded, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(syscall.GENERIC_READ)
	if flag == os.O_RDWR {
		access |= syscall.GENERIC_WRITE
	}
	handle, err := syscall.CreateFile(encoded, access, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(handle), path)
	var info syscall.ByHandleFileInformation
	h := syscall.Handle(f.Fd())
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		f.Close()
		return nil, err
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		f.Close()
		return nil, errSymlinkRefused
	}
	return f, nil
}

func createExclusive(path string, perm os.FileMode) (*os.File, error) {
	// O_EXCL refuses creation when anything (including a symlink) already
	// occupies the name, so a fresh exclusive create needs no extra guard.
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}
