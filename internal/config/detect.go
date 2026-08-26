// Detection of releasable packages in a working tree; mirrors the
// detectPackages/detectCargoPackages/readJsonName/readToml* helpers of
// src/config.ts. TOML reading is hand-rolled exactly like the TS
// readTomlSection/readTomlString/readTomlArray helpers — no TOML dependency.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/types"
)

// DetectPackages returns the raw PackageConfig candidates found in cwd:
// package.json (node), Cargo.toml root plus existing workspace members (rust),
// pyproject.toml [project] (python), and a version file (version-file, named
// after the cwd basename). Candidates are deduped by "type:path".
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
	for _, pkg := range candidates {
		key := fmt.Sprintf("%s:%s", pkg.Type, filepath.Clean(pkg.Path))
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, pkg)
	}
	return deduped, nil
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
	_, err := os.Stat(path)
	return err == nil
}

func readJSONName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
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
