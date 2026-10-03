package manifest

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/tomledit"
	"github.com/openhoo/hooversion/internal/types"
)

// ReadAtRef reads manifest and inherited workspace metadata from one immutable
// Git revision. Workspace resolution never consults the current checkout.
func ReadAtRef(cwd string, pkg types.NormalizedPackageConfig, ref string, baseEnv []string, contexts ...context.Context) (string, string, error) {
	relative := filepath.Clean(pkg.Manifest)
	if filepath.IsAbs(relative) {
		var err error
		relative, err = filepath.Rel(cwd, relative)
		if err != nil {
			return "", "", err
		}
	}
	if !filepath.IsLocal(relative) {
		return "", "", fmt.Errorf("historical manifest escapes repository: %s", pkg.Manifest)
	}
	read := func(path string) ([]byte, error) {
		if !filepath.IsLocal(path) {
			return nil, fmt.Errorf("historical workspace escapes repository: %s", path)
		}
		data, err := git.FileAtRefWithEnv(cwd, ref, path, baseEnv, contexts...)
		if err != nil {
			return nil, err
		}
		if len(data) > maxManifestBytes {
			return nil, fmt.Errorf("historical manifest %s exceeds %d bytes", path, maxManifestBytes)
		}
		return data, nil
	}
	data, err := read(relative)
	if err != nil {
		return "", "", err
	}
	if pkg.Type != types.PackageRust {
		return ReadData(pkg, data)
	}
	d, err := tomledit.Parse(data)
	if err != nil {
		return "", "", err
	}
	if d.Get("package", "version", "workspace") != true {
		return ReadData(pkg, data)
	}
	if explicit, ok := d.Text("package", "workspace"); ok {
		path := filepath.Clean(filepath.Join(filepath.Dir(relative), explicit, "Cargo.toml"))
		workspace, err := read(path)
		if err != nil {
			return "", "", err
		}
		return ReadDataWithWorkspace(pkg, data, workspace)
	}
	for dir := filepath.Dir(relative); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "Cargo.toml")
		workspace, readErr := read(candidate)
		if readErr == nil {
			w, err := tomledit.Parse(workspace)
			if err != nil {
				return "", "", fmt.Errorf("historical %s: %w", candidate, err)
			}
			if w.Get("workspace") != nil {
				return ReadDataWithWorkspace(pkg, data, workspace)
			}
		} else {
			for _, ctx := range contexts {
				if ctx != nil && ctx.Err() != nil {
					return "", "", ctx.Err()
				}
			}
		}
		if dir == "." || strings.TrimSpace(dir) == "" {
			break
		}
	}
	return "", "", fmt.Errorf("historical %s: %w", relative, ErrNoWorkspace)
}
