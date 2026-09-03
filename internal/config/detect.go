// Detection of releasable packages in a working tree; mirrors the
// detectPackages/detectCargoPackages/readJsonName/readToml* helpers of
// src/config.ts. TOML reading is hand-rolled exactly like the TS
// readTomlSection/readTomlString/readTomlArray helpers — no TOML dependency.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/safefs"
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
	text, err := os.ReadFile(root)
	if err != nil {
		return nil, err
	}
	content := string(text)

	var packages []types.PackageConfig
	if strings.Contains(content, "[package]") {
		name, err := readTomlName(root, "package")
		if err != nil {
			return nil, err
		}
		packages = append(packages, types.PackageConfig{Type: types.PackageRust, Path: ".", Name: name})
	}

	members := readTomlArray(content, "workspace", "members")
	for _, member := range members {
		manifest := filepath.Join(cwd, member, "Cargo.toml")
		if !fileExists(manifest) {
			continue
		}
		name, err := readTomlName(manifest, "package")
		if err != nil {
			return nil, err
		}
		path, err := normalizeRelative(member)
		if err != nil {
			return nil, err
		}
		packages = append(packages, types.PackageConfig{Type: types.PackageRust, Path: path, Name: name})
	}
	return packages, nil
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

func readTomlName(path, sectionName string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	section := readTomlSection(string(data), sectionName)
	name := readTomlString(section, "name")
	if name == "" {
		return "", errors.New("%s [%s] must contain a name", path, sectionName)
	}
	return name, nil
}

// readTomlSection collects the lines of `[sectionName]` up to the next
// heading, mirroring src/config.ts readTomlSection.
func readTomlSection(text, sectionName string) string {
	var section []string
	inSection := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		heading := tomlHeadingRE.FindStringSubmatch(line)
		if heading != nil {
			if inSection {
				break
			}
			inSection = heading[1] == sectionName
			continue
		}
		if inSection {
			section = append(section, line)
		}
	}
	return strings.Join(section, "\n")
}

var tomlHeadingRE = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)

func readTomlString(section, key string) string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*["']([^"']+)["']`)
	m := re.FindStringSubmatch(section)
	if m == nil {
		return ""
	}
	return m[1]
}

// readTomlArray reads a possibly multiline string array, mirroring
// src/config.ts readTomlArray including its single-line fast path.
func readTomlArray(text, sectionName, key string) []string {
	section := readTomlSection(text, sectionName)

	oneLineRE := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*\[([^\]]*)\]`)
	if m := oneLineRE.FindStringSubmatch(section); m != nil {
		return quotedStrings(m[1])
	}

	var result []string
	inArray := false
	entryRE := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=\s*\[`)
	for _, line := range strings.Split(section, "\n") {
		if !inArray && entryRE.MatchString(line) {
			inArray = true
		}
		if inArray {
			result = append(result, quotedStrings(line)...)
			if strings.Contains(line, "]") {
				break
			}
		}
	}
	return result
}

var quotedStringRE = regexp.MustCompile(`["']([^"']+)["']`)

func quotedStrings(s string) []string {
	var out []string
	for _, m := range quotedStringRE.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}
