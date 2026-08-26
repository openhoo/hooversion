//go:build linux

package githubapi

import (
	"os"
	"syscall"
)

// assetMetadata mirrors src/github.ts stat identity fields on Linux.
func assetMetadata(fi os.FileInfo) (releaseAssetMetadataT, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return releaseAssetMetadataT{}, false
	}
	return releaseAssetMetadataT{
		dev:       uint64(st.Dev),
		ino:       st.Ino,
		size:      fi.Size(),
		mtimeSec:  st.Mtim.Sec,
		mtimeNsec: st.Mtim.Nsec,
		ctimeSec:  st.Ctim.Sec,
		ctimeNsec: st.Ctim.Nsec,
	}, true
}
