// Package safefs centralizes the symlink-refusing file operations used by
// changelog updates, managed outputs, Cargo.lock rewrites, and release-asset
// reads (mirrors the O_NOFOLLOW discipline of src/*.ts on every platform).
package safefs

import (
	"os"
)

// OpenReadNoFollow opens path read-only without following a final symlink.
func OpenReadNoFollow(path string) (*os.File, error) {
	return openNoFollow(path, os.O_RDONLY)
}

// OpenReadWriteNoFollow opens path read-write without following a final symlink.
func OpenReadWriteNoFollow(path string) (*os.File, error) {
	return openNoFollow(path, os.O_RDWR)
}

// CreateExclusive creates path exclusively with O_WRONLY semantics, refusing
// to follow any existing entry (creation through an existing symlink fails).
func CreateExclusive(path string, perm os.FileMode) (*os.File, error) {
	return createExclusive(path, perm)
}
