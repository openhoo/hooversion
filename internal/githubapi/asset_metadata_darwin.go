//go:build darwin

package githubapi

import (
	"os"
	"syscall"
)

// assetMetadata mirrors src/github.ts stat identity fields on Darwin, whose
// Stat_t exposes Dev as int32 and timestamps as Mtimespec/Ctimespec.
func assetMetadata(fi os.FileInfo) (releaseAssetMetadataT, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return releaseAssetMetadataT{}, false
	}
	return releaseAssetMetadataT{
		dev:       uint64(st.Dev),
		ino:       st.Ino,
		size:      fi.Size(),
		mtimeSec:  st.Mtimespec.Sec,
		mtimeNsec: st.Mtimespec.Nsec,
		ctimeSec:  st.Ctimespec.Sec,
		ctimeNsec: st.Ctimespec.Nsec,
	}, true
}
