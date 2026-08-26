// Package githubapi — this file mirrors the release-asset safety chain of
// src/github.ts: path validation, upload-URL trust, O_NOFOLLOW regular-file
// reads with dev/ino/size/mtime stability double-checks and realpath
// containment inside the repository root.
package githubapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	hverr "github.com/openhoo/hooversion/internal/errors"
)

const maxReleaseAssetSizeBytes = 100 * 1024 * 1024

var uploadTemplateSuffixRE = regexp.MustCompile(`\{\?[^}]*\}$`)

var errUnsupportedStatMetadata = fmt.Errorf("platform does not expose inode metadata for release assets")

// UploadAsset uploads one release asset, mirroring src/github.ts:
// relative-only paths without parent traversal or NUL, an upload URL whose
// origin must equal the API origin (or uploads.github.com when the API origin
// is api.github.com), an O_NOFOLLOW open of a regular file of at most 100 MiB,
// realpath containment inside the repository root, and dev/ino/size/mtime
// stability checked before and during the read.
func (c *Client) UploadAsset(uploadURLTemplate, name, path string) error {
	if err := assertSafeReleaseAssetPath(path); err != nil {
		return err
	}
	uploadURL, err := c.validateUploadURL(uploadURLTemplate)
	if err != nil {
		return err
	}
	data, err := readValidatedReleaseAsset(path)
	if err != nil {
		return err
	}
	req, err := c.newRequest(http.MethodPost, uploadURL+"?name="+encodeURIComponent(name), "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := c.do(req, false)
	if err != nil {
		return err
	}
	drainAndClose(resp)
	return nil
}

// assertSafeReleaseAssetPath rejects empty, NUL-containing, absolute,
// win32-absolute, and parent-traversing asset paths exactly like
// assertSafeReleaseAssetPath in src/github.ts.
func assertSafeReleaseAssetPath(asset string) error {
	if asset == "" ||
		strings.ContainsRune(asset, '\x00') ||
		filepath.IsAbs(asset) ||
		windowsAbsolutePath(asset) ||
		hasParentComponent(asset) {
		return hverr.New("Release asset path must be relative without parent traversal: %s", asset)
	}
	return nil
}

// windowsAbsolutePath mirrors win32.isAbsolute: a leading separator (UNC or
// root-relative) or a drive letter followed by a separator is absolute.
func windowsAbsolutePath(asset string) bool {
	if len(asset) > 0 && asset[0] == '\\' {
		return true
	}
	if len(asset) >= 3 {
		c := asset[0]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if isLetter && asset[1] == ':' && (asset[2] == '\\' || asset[2] == '/') {
			return true
		}
	}
	return false
}

func hasParentComponent(asset string) bool {
	for _, comp := range strings.FieldsFunc(asset, func(r rune) bool { return r == '/' || r == '\\' }) {
		if comp == ".." {
			return true
		}
	}
	return false
}

// isContainedPath reports whether path is root itself or lies below root,
// mirroring isContainedPath in src/github.ts.
func isContainedPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." ||
		(rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func resolveReleaseAssetPath(root, asset string) (string, error) {
	path := filepath.Join(root, asset)
	if !isContainedPath(root, path) {
		return "", hverr.New("Release asset path escapes the repository: %s", asset)
	}
	return path, nil
}

// resolveAssetRoot mirrors readMissingReleaseAssets' cwd resolution: the
// symlink-free absolute root must exist and be a directory.
func resolveAssetRoot(wd string) (string, error) {
	root, err := filepath.EvalSymlinks(wd)
	if err == nil {
		if fi, statErr := os.Lstat(root); statErr == nil {
			if fi.IsDir() {
				return root, nil
			}
			return "", hverr.New("Release asset root is not a directory: %s", wd)
		}
	}
	return "", hverr.New("Could not resolve release asset root: %s", wd)
}

type releaseAssetMetadataT struct {
	dev       uint64
	ino       uint64
	size      int64
	mtimeSec  int64
	mtimeNsec int64
	ctimeSec  int64
	ctimeNsec int64
}

// releaseAssetMetadata captures the identity fields compared across reads;
// mtime/ctime use second+nanosecond pairs for exact comparison like
// mtimeMs/ctimeMs equality in src/github.ts. ok is false on platforms that do
// not expose inode metadata through os.FileInfo.
func releaseAssetMetadata(fi os.FileInfo) (meta releaseAssetMetadataT, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return releaseAssetMetadataT{}, false
	}
	return releaseAssetMetadataT{
		dev:       st.Dev,
		ino:       st.Ino,
		size:      fi.Size(),
		mtimeSec:  st.Mtim.Sec,
		mtimeNsec: st.Mtim.Nsec,
		ctimeSec:  st.Ctim.Sec,
		ctimeNsec: st.Ctim.Nsec,
	}, true
}

func sameReleaseAssetMetadata(left, right releaseAssetMetadataT) bool {
	return left.dev == right.dev &&
		left.ino == right.ino &&
		left.size == right.size &&
		left.mtimeSec == right.mtimeSec &&
		left.mtimeNsec == right.mtimeNsec &&
		left.ctimeSec == right.ctimeSec &&
		left.ctimeNsec == right.ctimeNsec
}

func changedWhileReading(asset string) *hverr.ExitError {
	return hverr.New("Release asset changed while it was being read: %s", asset)
}

func wrapInsecureRead(asset string, err error) *hverr.ExitError {
	return hverr.New("Could not securely read release asset %s: %v", asset, err)
}

// assertRegularReleaseAsset mirrors assertRegularReleaseAsset in src/github.ts.
func assertRegularReleaseAsset(fi os.FileInfo, asset string) error {
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return hverr.New("Release asset must be a regular file, not a symbolic link: %s", asset)
	}
	if fi.Size() < 0 || fi.Size() > maxReleaseAssetSizeBytes {
		return hverr.New("Release asset exceeds the %d byte upload limit: %s", maxReleaseAssetSizeBytes, asset)
	}
	return nil
}

// assertStableReleaseAssetPath mirrors assertStableReleaseAssetPath in
// src/github.ts: realpath containment, no symlink traversal anywhere along
// the path, and unchanged lstat metadata versus expected.
func assertStableReleaseAssetPath(root, path, asset string, expected releaseAssetMetadataT) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return wrapInsecureRead(asset, err)
	}
	if !isContainedPath(root, canonical) {
		return hverr.New("Release asset path escapes the repository: %s", asset)
	}
	if canonical != path {
		return hverr.New("Release asset path must not traverse a symbolic link: %s", asset)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return wrapInsecureRead(asset, err)
	}
	if err := assertRegularReleaseAsset(fi, asset); err != nil {
		return err
	}
	meta, ok := releaseAssetMetadata(fi)
	if !ok {
		return wrapInsecureRead(asset, errUnsupportedStatMetadata)
	}
	if !sameReleaseAssetMetadata(expected, meta) {
		return changedWhileReading(asset)
	}
	return nil
}

// readExactly fills size bytes from f; any early EOF means the file shrank
// under us, mirroring readReleaseAssetDescriptor in src/github.ts. Real IO
// errors are returned unwrapped so the caller applies the insecure-read wrap.
func readExactly(f *os.File, size int64, asset string) ([]byte, error) {
	data := make([]byte, size)
	offset := 0
	for offset < len(data) {
		n, err := f.Read(data[offset:])
		if err != nil && err != io.EOF {
			return nil, err
		}
		if n == 0 {
			return nil, changedWhileReading(asset)
		}
		offset += n
	}
	return data, nil
}

// readValidatedReleaseAsset mirrors the descriptor-only validated reader of
// src/github.ts anchored at the process working directory (the CLI's repo
// root): lexical containment, O_NOFOLLOW open, regular-file + size checks,
// path-stability before and after the read, exact-length read, and metadata
// stability across the read plus one final descriptor check immediately
// before upload.
func readValidatedReleaseAsset(asset string) ([]byte, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, hverr.New("Could not resolve release asset root: .")
	}
	root, err := resolveAssetRoot(wd)
	if err != nil {
		return nil, err
	}
	path, err := resolveReleaseAssetPath(root, asset)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, wrapInsecureRead(asset, err)
	}
	defer f.Close()

	descFi, err := f.Stat()
	if err != nil {
		return nil, wrapInsecureRead(asset, err)
	}
	if err := assertRegularReleaseAsset(descFi, asset); err != nil {
		return nil, err
	}
	descMeta, ok := releaseAssetMetadata(descFi)
	if !ok {
		return nil, wrapInsecureRead(asset, errUnsupportedStatMetadata)
	}
	if err := assertStableReleaseAssetPath(root, path, asset, descMeta); err != nil {
		return nil, err
	}

	data, err := readExactly(f, descMeta.size, asset)
	if err != nil {
		if _, isExit := err.(*hverr.ExitError); isExit {
			return nil, err
		}
		return nil, wrapInsecureRead(asset, err)
	}

	afterFi, err := f.Stat()
	if err != nil {
		return nil, wrapInsecureRead(asset, err)
	}
	afterMeta, ok := releaseAssetMetadata(afterFi)
	if !ok {
		return nil, wrapInsecureRead(asset, errUnsupportedStatMetadata)
	}
	if !sameReleaseAssetMetadata(descMeta, afterMeta) || int64(len(data)) != descMeta.size {
		return nil, changedWhileReading(asset)
	}
	if err := assertStableReleaseAssetPath(root, path, asset, afterMeta); err != nil {
		return nil, err
	}

	finalFi, err := f.Stat()
	if err != nil {
		return nil, wrapInsecureRead(asset, err)
	}
	finalMeta, ok := releaseAssetMetadata(finalFi)
	if !ok || !sameReleaseAssetMetadata(afterMeta, finalMeta) {
		return nil, changedWhileReading(asset)
	}
	return data, nil
}

// validateUploadURL mirrors validateGitHubUploadUrl in src/github.ts: strips
// the "{?name,label}" template suffix, rejects leftover braces, requires an
// absolute https URL without userinfo, explicit port, query, or fragment, and
// enforces the origin trust rule against this client's BaseURL.
func (c *Client) validateUploadURL(template string) (string, error) {
	invalid := hverr.New("Invalid GitHub release upload URL: %s", template)

	uploadURL := uploadTemplateSuffixRE.ReplaceAllString(template, "")
	if strings.ContainsAny(uploadURL, "{}") {
		return "", invalid
	}
	parsed, err := url.Parse(uploadURL)
	if err != nil || !parsed.IsAbs() {
		return "", invalid
	}

	var authority string
	if idx := strings.Index(uploadURL, "//"); idx >= 0 {
		authority = uploadURL[idx+2:]
	}
	if cut := strings.IndexAny(authority, "/?#"); cut >= 0 {
		authority = authority[:cut]
	}
	host := authority
	if idx := strings.LastIndex(authority, "@"); idx >= 0 {
		host = authority[idx+1:]
	}
	hasExplicitPort := false
	if strings.HasPrefix(host, "[") {
		hasExplicitPort = strings.Contains(host, "]:")
	} else {
		hasExplicitPort = strings.Contains(host, ":")
	}

	if parsed.Scheme != "https" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		hasExplicitPort {
		return "", invalid
	}

	apiOrigin := originOf(c.BaseURL)
	uploadOrigin := parsed.Scheme + "://" + parsed.Host
	trusted := uploadOrigin == apiOrigin ||
		(apiOrigin == "https://api.github.com" && uploadOrigin == "https://uploads.github.com")
	if !trusted {
		return "", hverr.New("Untrusted GitHub release upload URL: %s", template)
	}
	return parsed.String(), nil
}

func originOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return parsed.Scheme + "://" + parsed.Host
}
