//go:build unix && !linux && !darwin

package githubapi

import (
	"os"
	"syscall"
)

// assetMetadata covers the remaining Unix platforms whose Stat_t follows the
// BSD/Solaris Mtim/Ctim layout.
func assetMetadata(fi os.FileInfo) (releaseAssetMetadataT, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return releaseAssetMetadataT{}, false
	}
	return releaseAssetMetadataT{
		dev:       uint64(st.Dev),
		ino:       st.Ino,
		size:      fi.Size(),
		mtimeSec:  int64(st.Mtim.Sec),
		mtimeNsec: int64(st.Mtim.Nsec),
		ctimeSec:  int64(st.Ctim.Sec),
		ctimeNsec: int64(st.Ctim.Nsec),
	}, true
}
