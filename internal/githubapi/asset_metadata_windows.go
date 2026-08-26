//go:build windows

package githubapi

import (
	"os"
	"syscall"
)

// assetMetadata derives stability fields from Win32FileAttributeData. Windows
// exposes no dev/ino through FileInfo; they stay zero and comparison still
// detects creation/write-time or size mutation. Symlink-identity protection is
// enforced by the platform-independent canonical-path checks in upload.go.
func assetMetadata(fi os.FileInfo) (releaseAssetMetadataT, bool) {
	data, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return releaseAssetMetadataT{}, false
	}
	return releaseAssetMetadataT{
		size:      fi.Size(),
		mtimeSec:  filetimeToUnix(data.LastWriteTime),
		mtimeNsec: filetimeNsec(data.LastWriteTime),
		ctimeSec:  filetimeToUnix(data.CreationTime),
		ctimeNsec: filetimeNsec(data.CreationTime),
	}, true
}

// filetimeToUnix converts a FILETIME (100ns ticks since 1601-01-01) to Unix seconds.
func filetimeToUnix(ft syscall.Filetime) int64 {
	ticks := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	return ticks/1e7 - 11644473600
}

// filetimeNsec returns the sub-second remainder of a FILETIME in nanoseconds.
func filetimeNsec(ft syscall.Filetime) int64 {
	ticks := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	return (ticks % 1e7) * 100
}
