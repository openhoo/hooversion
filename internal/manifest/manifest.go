// Package manifest reads and rewrites package manifests (package.json,
// Cargo.toml, pyproject.toml, version files) including local dependency
// edges and Cargo.lock. TOML edits preserve unrelated source text; filesystem
// operations may be pinned to a repository root for release execution.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	hverrors "github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/tomledit"
	"github.com/openhoo/hooversion/internal/types"
)

const maxManifestBytes = 16 << 20

// Read returns the package name and current version from the package
// manifest (mirrors readManifest).
func Read(pkg types.NormalizedPackageConfig) (string, string, error) {
	return ReadWithFS(pkg, safefs.Native{})
}
func ReadWithFS(pkg types.NormalizedPackageConfig, files safefs.FileSystem) (string, string, error) {
	data, err := files.ReadRegularFile(pkg.Manifest, maxManifestBytes)
	if err != nil {
		return "", "", err
	}

	if pkg.Type == types.PackageRust {
		d, err := tomledit.Parse(data)
		if err != nil {
			return "", "", err
		}
		if d.Get("package", "version", "workspace") == true {
			root, err := workspaceManifestFS(pkg.Manifest, files)
			if err != nil {
				return "", "", err
			}
			workspace, err := files.ReadRegularFile(root, maxManifestBytes)
			if err != nil {
				return "", "", err
			}
			return ReadDataWithWorkspace(pkg, data, workspace)
		}
	}
	return ReadData(pkg, data)
}

// ReadData parses manifest bytes using the same format rules as Read. The
// manifest path is used only for diagnostics and is never opened.
func ReadData(pkg types.NormalizedPackageConfig, data []byte) (string, string, error) {
	switch pkg.Type {
	case types.PackageNode:
		return readPackageJSONData(pkg.Manifest, data)
	case types.PackageRust:
		return readTomlPackageData(pkg.Manifest, string(data), "package")
	case types.PackageVersionFile:
		return readVersionFileData(pkg.Manifest, pkg.Name, data)
	default:
		return readTomlPackageData(pkg.Manifest, string(data), "project")
	}
}

// UpdateVersion rewrites the manifest version field (mirrors
// updateManifestVersion). Node manifests are re-emitted as 2-space JSON
// with a trailing newline preserving document key order.
func UpdateVersion(pkg types.NormalizedPackageConfig, next string) error {
	return UpdateVersionWithFS(pkg, next, safefs.Native{})
}
func UpdateVersionWithFS(pkg types.NormalizedPackageConfig, next string, files safefs.FileSystem) error {
	path := pkg.Manifest
	switch pkg.Type {
	case types.PackageNode:
		data, err := files.ReadRegularFile(path, maxManifestBytes)
		if err != nil {
			return err
		}
		root, err := decodeOrderedJSON(data)
		if err != nil {
			return err
		}
		root.set("version", next)
		return files.WriteFileAtomic(path, marshalOrderedJSON(root), 0o644)
	case types.PackageVersionFile:
		return files.WriteFileAtomic(path, []byte(next+"\n"), 0o644)
	}
	section := "project"
	if pkg.Type == types.PackageRust {
		section = "package"
	}
	if err := updateTomlSectionVersion(path, section, next, files); err != nil {
		return err
	}
	if pkg.Type == types.PackageRust {
		root, err := workspaceManifestFS(path, files)
		if err != nil {
			if !errors.Is(err, ErrNoWorkspace) {
				return err
			}
			root = path
		}
		return updateCargoLock(filepath.Dir(root), map[string]string{pkg.Name: next}, files)
	}
	return nil
}

// UpdateLocalDependencyVersions rewrites dependency edges of pkg that point
// at released packages. versions maps released package name to its next
// version; only names declared in pkg.Dependencies are considered, matched
// case-insensitively against the map keys. Rust packages additionally get
// their workspace root [workspace.dependencies] and Cargo.lock updated
// (both ENOENT-tolerant). Mirrors updateLocalDependencyVersions scoped to a
// single package.
func UpdateLocalDependencyVersions(cwd string, pkg types.NormalizedPackageConfig, versions map[string]string) error {
	return UpdateLocalDependencyVersionsWithFS(cwd, pkg, versions, safefs.Native{})
}
func UpdateLocalDependencyVersionsWithFS(cwd string, pkg types.NormalizedPackageConfig, versions map[string]string, files safefs.FileSystem) error {
	localVersions := map[string]string{}
	for _, dependency := range pkg.Dependencies {
		if target, ok := dependencyReleasedName(pkg.Type, dependency, versions); ok {
			localVersions[target] = versions[target]
		}
	}
	if len(localVersions) == 0 {
		return nil
	}

	path := filepath.Join(cwd, pkg.Manifest)
	switch pkg.Type {
	case types.PackageNode:
		return updateNodeLocalDependencies(path, pkg, localVersions, files)
	case types.PackagePython:
		return updatePythonLocalDependencies(path, pkg, localVersions, files)
	case types.PackageRust:
		if err := updateRustDependencyTables(path, &pkg, localVersions, false, files); err != nil {
			return err
		}
		root, rootErr := workspaceManifestFS(path, files)
		if rootErr != nil {
			if !errors.Is(rootErr, ErrNoWorkspace) {
				return rootErr
			}
			root = path
		}
		if err := updateRustDependencyTables(root, nil, localVersions, true, files); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return updateCargoLock(filepath.Dir(root), localVersions, files)
	}
	return nil
}

// --- ordered JSON (stands in for JSON.parse / JSON.stringify(v, null, 2)) --

type jsonObject struct {
	keys []string
	vals map[string]any
}

func (o *jsonObject) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// set overwrites an existing key in place or appends a new key at the end,
// matching JS object assignment semantics.
func (o *jsonObject) set(key string, value any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

func decodeOrderedJSON(data []byte) (*jsonObject, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	root, err := decodeJSONValue(dec)
	if err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("manifest must contain exactly one JSON value")
	}
	obj, ok := root.(*jsonObject)
	if !ok {
		return nil, errors.New("manifest root must be a JSON object")
	}
	return obj, nil
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeJSONToken(dec, tok)
}

func decodeJSONToken(dec *json.Decoder, tok json.Token) (any, error) {
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			obj := &jsonObject{vals: map[string]any{}}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key := keyTok.(string)
				value, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				obj.set(key, value)
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return obj, nil
		case '[':
			var arr []any
			for dec.More() {
				value, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, value)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return tok, nil
}

// marshalOrderedJSON renders the document exactly like
// JSON.stringify(value, null, 2) plus a trailing newline: two-space indent,
// no HTML escaping, numbers verbatim.
func marshalOrderedJSON(root *jsonObject) []byte {
	var sb strings.Builder
	encodeJSONValue(&sb, root, 0)
	sb.WriteString("\n")
	return []byte(sb.String())
}

func encodeJSONValue(sb *strings.Builder, value any, depth int) {
	switch v := value.(type) {
	case *jsonObject:
		if len(v.keys) == 0 {
			sb.WriteString("{}")
			return
		}
		sb.WriteString("{\n")
		pad := strings.Repeat("  ", depth+1)
		for i, key := range v.keys {
			if i > 0 {
				sb.WriteString(",\n")
			}
			sb.WriteString(pad)
			encodeJSONString(sb, key)
			sb.WriteString(": ")
			encodeJSONValue(sb, v.vals[key], depth+1)
		}
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("  ", depth))
		sb.WriteString("}")
	case []any:
		if len(v) == 0 {
			sb.WriteString("[]")
			return
		}
		sb.WriteString("[\n")
		pad := strings.Repeat("  ", depth+1)
		for i, item := range v {
			if i > 0 {
				sb.WriteString(",\n")
			}
			sb.WriteString(pad)
			encodeJSONValue(sb, item, depth+1)
		}
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("  ", depth))
		sb.WriteString("]")
	case string:
		encodeJSONString(sb, v)
	case json.Number:
		sb.WriteString(v.String())
	case bool:
		if v {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	default:
		sb.WriteString("null")
	}
}

func encodeJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
}

// --- shared helpers ---------------------------------------------------------

func normalizePackageName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// findReleasedName matches name case-insensitively against the released
// version map keys and returns the canonical map key.
func findReleasedName(name string, released map[string]string) (string, bool) {
	normalized := normalizePackageName(name)
	keys := make([]string, 0, len(released))
	for key := range released {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if normalizePackageName(key) == normalized {
			return key, true
		}
	}
	return "", false
}

func sortedKeys(released map[string]string) []string {
	keys := make([]string, 0, len(released))
	for key := range released {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func assertAllDependenciesFound(path, ownerName string, released map[string]string, found map[string]bool) error {
	for _, target := range sortedKeys(released) {
		if !found[target] {
			return hverrors.New("%s package %s declares local dependency %s, but it was not found", path, ownerName, target)
		}
	}
	return nil
}

// --- node -------------------------------------------------------------------

var nodeDependencySections = []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"}

var (
	nodeUnsupportedRE = regexp.MustCompile(`^(?:file|git|https?):`)
	nodePrefixRE      = regexp.MustCompile(`^[~^]`)
)

func rewriteNodeRequirement(current, version, path, name string) (string, error) {
	if strings.HasPrefix(current, "workspace:") {
		return current, nil
	}
	if nodeUnsupportedRE.MatchString(current) {
		return "", hverrors.New("%s dependency %s has unsupported specifier %s", path, name, current)
	}
	prefix := ""
	if m := nodePrefixRE.FindString(current); m != "" {
		prefix = m
	}
	return prefix + version, nil
}

func updateNodeLocalDependencies(path string, owner types.NormalizedPackageConfig, released map[string]string, files safefs.FileSystem) error {
	data, err := files.ReadRegularFile(path, maxManifestBytes)
	if err != nil {
		return err
	}
	root, err := decodeOrderedJSON(data)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	changed := false

	for _, section := range nodeDependencySections {
		value, ok := root.get(section)
		if !ok {
			continue
		}
		obj, isObj := value.(*jsonObject)
		if !isObj {
			return hverrors.New("%s %s must be an object", path, section)
		}
		for _, name := range obj.keys {
			target, ok := findReleasedName(name, released)
			if !ok {
				continue
			}
			found[target] = true
			current, isStr := obj.vals[name].(string)
			if !isStr {
				return hverrors.New("%s package %s has unsupported dependency %s", path, owner.Name, name)
			}
			next, err := rewriteNodeRequirement(current, released[target], path, name)
			if err != nil {
				return err
			}
			if next != current {
				obj.set(name, next)
				changed = true
			}
		}
	}

	if err := assertAllDependenciesFound(path, owner.Name, released, found); err != nil {
		return err
	}
	if changed {
		return files.WriteFileAtomic(path, marshalOrderedJSON(root), 0o644)
	}
	return nil
}

// --- TOML [package]/[project] reading ---------------------------------------

func readPackageJSONData(path string, data []byte) (string, string, error) {
	root, err := decodeOrderedJSON(data)
	if err != nil {
		return "", "", err
	}
	nameValue, _ := root.get("name")
	versionValue, _ := root.get("version")
	name, nameOK := nameValue.(string)
	version, versionOK := versionValue.(string)
	if !nameOK || name == "" || !versionOK || version == "" {
		return "", "", hverrors.New("%s must contain name and version", path)
	}
	return name, version, nil
}

func readVersionFileData(path, name string, data []byte) (string, string, error) {
	version := strings.TrimSpace(string(data))
	if version == "" {
		return "", "", hverrors.New("%s must contain a version", path)
	}
	return name, version, nil
}
