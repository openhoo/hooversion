// Detection of releasable packages from validated JSON and TOML manifests.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/openhoo/hooversion/internal/cargoworkspace"
	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/tomledit"
	"github.com/openhoo/hooversion/internal/types"
)

const maxPackageJSONBytes = 1 << 20

var openJSONNoFollow = safefs.OpenReadNoFollow

// DetectPackages returns the raw PackageConfig candidates found in cwd:
// package.json files (node), Cargo.toml root plus existing workspace members
// (rust), pyproject.toml [project] (python), and a version file (version-file,
// named after the cwd basename). Node manifests below ignored/generated
// directories are skipped. Candidates are deduped by "type:path".
func DetectPackages(cwd string) ([]types.PackageConfig, error) {
	var candidates []types.PackageConfig

	packageJSON := filepath.Join(cwd, "package.json")
	if fileExists(packageJSON) {
		name, err := readJSONName(packageJSON)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, types.PackageConfig{Type: types.PackageNode, Path: ".", Name: name})
	}
	nestedNodePackages, err := detectNestedNodePackages(cwd)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, nestedNodePackages...)

	cargoToml := filepath.Join(cwd, "Cargo.toml")
	if fileExists(cargoToml) {
		cargo, err := detectCargoPackages(cwd)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, cargo...)
	}

	pyprojectToml := filepath.Join(cwd, "pyproject.toml")
	if fileExists(pyprojectToml) {
		name, err := readTomlName(pyprojectToml, "project")
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, types.PackageConfig{Type: types.PackagePython, Path: ".", Name: name})
	}

	nestedPython, err := detectNestedPythonPackages(cwd)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, nestedPython...)

	versionFile := filepath.Join(cwd, "version")
	if fileExists(versionFile) {
		candidates = append(candidates, types.PackageConfig{Type: types.PackageVersionFile, Path: ".", Name: filepath.Base(cwd)})
	}

	seen := make(map[string]bool, len(candidates))
	deduped := candidates[:0]
	for index := range candidates {
		normalizedPath, err := normalizeRelative(candidates[index].Path)
		if err != nil {
			return nil, err
		}
		candidates[index].Path = normalizedPath
		key := fmt.Sprintf("%s:%s", candidates[index].Type, normalizedPath)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, candidates[index])
	}
	return deduped, nil
}

var ignoredNodePackageDirs = map[string]struct{}{
	".git":         {},
	".github":      {},
	".hg":          {},
	".hooversion":  {},
	".svn":         {},
	"node_modules": {},
	"vendor":       {},
}

func detectNestedNodePackages(cwd string) ([]types.PackageConfig, error) {
	var packages []types.PackageConfig
	err := filepath.WalkDir(cwd, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != cwd {
				if _, ignored := ignoredNodePackageDirs[entry.Name()]; ignored {
					return fs.SkipDir
				}
			}
			return nil
		}
		if entry.Name() != "package.json" || filepath.Dir(path) == cwd || !fileExists(path) {
			return nil
		}
		name, err := readJSONName(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(cwd, filepath.Dir(path))
		if err != nil {
			return err
		}
		packagePath, err := normalizeRelative(rel)
		if err != nil {
			return err
		}
		packages = append(packages, types.PackageConfig{
			Type: types.PackageNode,
			Path: packagePath,
			Name: name,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return packages, nil
}

// DefaultManifestPath mirrors defaultManifestPath in src/manifest.ts; python
// is the fallthrough type.
func DefaultManifestPath(t types.PackageType, pkgPath string) string {
	switch t {
	case types.PackageNode:
		return filepath.Join(pkgPath, "package.json")
	case types.PackageRust:
		return filepath.Join(pkgPath, "Cargo.toml")
	case types.PackageVersionFile:
		return filepath.Join(pkgPath, "version")
	default:
		return filepath.Join(pkgPath, "pyproject.toml")
	}
}

func detectCargoPackages(cwd string) ([]types.PackageConfig, error) {
	root := filepath.Join(cwd, "Cargo.toml")
	data, err := safefs.ReadRegularFile(root, maxPackageJSONBytes)
	if err != nil {
		return nil, err
	}
	d, err := tomledit.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", root, err)
	}
	var packages []types.PackageConfig
	if d.Get("package") != nil {
		name, ok := d.Text("package", "name")
		if !ok || name == "" {
			return nil, fmt.Errorf("%s [package] must contain a name", root)
		}
		packages = append(packages, types.PackageConfig{Type: types.PackageRust, Path: ".", Name: name})
	}
	members, err := tomlStringArray(d.Get("workspace", "members"))
	if err != nil {
		return nil, fmt.Errorf("%s workspace.members: %w", root, err)
	}
	excludes, err := tomlStringArray(d.Get("workspace", "exclude"))
	if err != nil {
		return nil, fmt.Errorf("%s workspace.exclude: %w", root, err)
	}
	excluded := map[string]bool{}
	for _, pattern := range excludes {
		paths, err := cargoMemberPaths(cwd, pattern)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			excluded[p] = true
		}
	}
	seen := map[string]bool{}
	for _, pattern := range members {
		paths, err := cargoMemberPaths(cwd, pattern)
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("cargo workspace member %q matched no directories", pattern)
		}
		for _, path := range paths {
			if excluded[path] || seen[path] {
				continue
			}
			seen[path] = true
			manifest := filepath.Join(cwd, path, "Cargo.toml")
			if err := safefs.RequireContainedPath(cwd, filepath.Join(path, "Cargo.toml")); err != nil {
				return nil, err
			}
			name, err := readTomlName(manifest, "package")
			if err != nil {
				return nil, err
			}
			packages = append(packages, types.PackageConfig{Type: types.PackageRust, Path: path, Name: name})
		}
	}
	return packages, nil
}
func tomlStringArray(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a string array")
	}
	result := make([]string, 0, len(array))
	for _, entry := range array {
		s, ok := entry.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string array")
		}
		result = append(result, s)
	}
	return result, nil
}
func cargoMemberPaths(cwd, pattern string) ([]string, error) {
	return cargoworkspace.Expand(cwd, pattern)
}

func fileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func readJSONName(path string) (string, error) {
	file, err := openJSONNoFollow(path)
	if err != nil {
		return "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return "", err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return "", errors.New("%s must be a regular file", path)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxPackageJSONBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(data) > maxPackageJSONBytes {
		return "", errors.New("%s exceeds the maximum package.json size", path)
	}
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", errors.New("%s must contain valid JSON: %v", path, err)
	}
	if doc.Name == "" {
		return "", errors.New("%s must contain a name", path)
	}
	return doc.Name, nil
}

func readTomlName(path, section string) (string, error) {
	data, err := safefs.ReadRegularFile(path, maxPackageJSONBytes)
	if err != nil {
		return "", err
	}
	d, err := tomledit.Parse(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	name, ok := d.Text(section, "name")
	if section == "project" && !ok {
		name, ok = d.Text("tool", "poetry", "name")
	}
	if !ok || name == "" {
		return "", fmt.Errorf("%s [%s] must contain a name", path, section)
	}
	return name, nil
}

func detectNestedPythonPackages(cwd string) ([]types.PackageConfig, error) {
	var packages []types.PackageConfig
	err := filepath.WalkDir(cwd, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != cwd {
				if _, ignored := ignoredNodePackageDirs[entry.Name()]; ignored {
					return fs.SkipDir
				}
				switch entry.Name() {
				case ".venv", "venv", "__pycache__", ".tox", ".nox":
					return fs.SkipDir
				}
			}
			return nil
		}
		if entry.Name() != "pyproject.toml" || filepath.Dir(path) == cwd || !fileExists(path) {
			return nil
		}
		data, err := safefs.ReadRegularFile(path, maxPackageJSONBytes)
		if err != nil {
			return err
		}
		d, err := tomledit.Parse(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		name, ok := d.Text("project", "name")
		if !ok {
			name, ok = d.Text("tool", "poetry", "name")
		}
		if !ok {
			return nil
		}
		if name == "" {
			return fmt.Errorf("%s must contain a project name", path)
		}
		rel, err := filepath.Rel(cwd, filepath.Dir(path))
		if err != nil {
			return err
		}
		packages = append(packages, types.PackageConfig{Type: types.PackagePython, Path: filepath.ToSlash(rel), Name: name})
		return nil
	})
	return packages, err
}
