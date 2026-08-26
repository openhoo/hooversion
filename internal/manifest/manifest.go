// Package manifest reads and rewrites package manifests (package.json,
// Cargo.toml, pyproject.toml, version files) including local dependency
// edges and Cargo.lock. Behavior mirrors src/manifest.ts 1:1; manifest
// paths are opened exactly as given on pkg.Manifest.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode"

	hverrors "github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/types"
)

// Read returns the package name and current version from the package
// manifest (mirrors readManifest).
func Read(pkg types.NormalizedPackageConfig) (string, string, error) {
	path := pkg.Manifest
	switch pkg.Type {
	case types.PackageNode:
		return readPackageJSON(path)
	case types.PackageRust:
		return readTomlPackage(path, "package")
	case types.PackageVersionFile:
		return readVersionFile(path, pkg.Name)
	default:
		return readTomlPackage(path, "project")
	}
}

// UpdateVersion rewrites the manifest version field (mirrors
// updateManifestVersion). Node manifests are re-emitted as 2-space JSON
// with a trailing newline preserving document key order.
func UpdateVersion(pkg types.NormalizedPackageConfig, next string) error {
	path := pkg.Manifest
	switch pkg.Type {
	case types.PackageNode:
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		root, err := decodeOrderedJSON(data)
		if err != nil {
			return err
		}
		root.set("version", next)
		return os.WriteFile(path, marshalOrderedJSON(root), 0o644)
	case types.PackageVersionFile:
		return os.WriteFile(path, []byte(next+"\n"), 0o644)
	}
	section := "project"
	if pkg.Type == types.PackageRust {
		section = "package"
	}
	return updateTomlSectionVersion(path, section, next)
}

// UpdateLocalDependencyVersions rewrites dependency edges of pkg that point
// at released packages. versions maps released package name to its next
// version; only names declared in pkg.Dependencies are considered, matched
// case-insensitively against the map keys. Rust packages additionally get
// their workspace root [workspace.dependencies] and Cargo.lock updated
// (both ENOENT-tolerant). Mirrors updateLocalDependencyVersions scoped to a
// single package.
func UpdateLocalDependencyVersions(cwd string, pkg types.NormalizedPackageConfig, versions map[string]string) error {
	localVersions := map[string]string{}
	for _, dependency := range pkg.Dependencies {
		if target, ok := findReleasedName(dependency, versions); ok {
			localVersions[target] = versions[target]
		}
	}
	if len(localVersions) == 0 {
		return nil
	}

	path := filepath.Join(cwd, pkg.Manifest)
	switch pkg.Type {
	case types.PackageNode:
		return updateNodeLocalDependencies(path, pkg, localVersions)
	case types.PackagePython:
		return updatePythonLocalDependencies(path, pkg, localVersions)
	case types.PackageRust:
		if err := updateRustDependencyTables(path, &pkg, localVersions, false); err != nil {
			return err
		}
		if err := updateRustDependencyTables(filepath.Join(cwd, "Cargo.toml"), nil, localVersions, true); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return updateCargoLock(cwd, localVersions)
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

// splitLines mirrors text.split(/\r?\n/): CRLF pairs are separators, lone CR
// characters stay inside lines.
func splitLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// mapQuotedSegments walks line and invokes fn for every '...'/\"...\" span
// whose content holds no quote characters — the same spans matched by
// /(["'])([^"']*)\1/g — substituting fn's result (still quote-wrapped by the
// caller contract below) only when fn reports a change.
func mapQuotedSegments(line string, fn func(content string) (string, bool)) string {
	var sb strings.Builder
	i := 0
	for i < len(line) {
		c := line[i]
		if c != '"' && c != '\'' {
			sb.WriteByte(c)
			i++
			continue
		}
		close := -1
		for k := i + 1; k < len(line); k++ {
			ch := line[k]
			if ch == '"' || ch == '\'' {
				if ch == c {
					close = k
				}
				break
			}
		}
		if close < 0 {
			sb.WriteByte(c)
			i++
			continue
		}
		content := line[i+1 : close]
		if repl, changed := fn(content); changed {
			sb.WriteString(repl)
		} else {
			sb.WriteString(line[i : close+1])
		}
		i = close + 1
	}
	return sb.String()
}

var (
	tomlHeadingRE    = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	tomlVersionKeyRE = regexp.MustCompile(`^\s*version\s*=`)
)

// replaceFirstTomlValue swaps the first `= "..."`-style quoted value,
// mirroring String.replace with a non-global regex.
var tomlAssignValueRE = regexp.MustCompile(`=\s*["'][^"']+["']`)

func replaceFirstTomlValue(line, version string) string {
	loc := tomlAssignValueRE.FindStringIndex(line)
	if loc == nil {
		return line
	}
	return line[:loc[0]] + `= "` + version + `"` + line[loc[1]:]
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

func updateNodeLocalDependencies(path string, owner types.NormalizedPackageConfig, released map[string]string) error {
	data, err := os.ReadFile(path)
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
		return os.WriteFile(path, marshalOrderedJSON(root), 0o644)
	}
	return nil
}

// --- python -----------------------------------------------------------------

func isPythonDependencySection(section string) bool {
	return section == "project" ||
		section == "project.optional-dependencies" ||
		strings.HasPrefix(section, "project.optional-dependencies.") ||
		section == "tool.poetry.dependencies" ||
		(strings.HasPrefix(section, "tool.poetry.group.") && strings.HasSuffix(section, ".dependencies"))
}

var pythonNameRE = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)

var pythonAssignmentRE = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+)\s*=\s*(.*)$`)

func rewritePythonRequirementsLine(line string, released map[string]string, found map[string]bool, path string) (string, bool, error) {
	changed := false
	cbErr := error(nil)
	out := mapQuotedSegments(line, func(requirement string) (string, bool) {
		if cbErr != nil {
			return requirement, false
		}
		m := pythonNameRE.FindStringSubmatch(requirement)
		if m == nil {
			return requirement, false
		}
		target, ok := findReleasedName(m[1], released)
		if !ok {
			return requirement, false
		}
		found[target] = true
		next, err := rewritePythonRequirement(requirement, released[target], path, m[1])
		if err != nil {
			cbErr = err
			return requirement, false
		}
		if next == requirement {
			return requirement, false
		}
		changed = true
		return `"` + next + `"`, true
	})
	return out, changed, cbErr
}

func rewritePythonRequirement(requirement, version, path, name string) (string, error) {
	suffix := requirement[len(name):]
	if strings.HasPrefix(strings.TrimLeftFunc(suffix, unicode.IsSpace), "@") {
		return "", hverrors.New("%s dependency %s has unsupported direct URL syntax", path, name)
	}
	constraint, err := rewritePythonConstraint(suffix, version, path, name)
	if err != nil {
		return "", err
	}
	return name + constraint, nil
}

var pythonConstraintRE = regexp.MustCompile(`([<>=!~]{1,3})\s*([0-9][^,\s;]*)`)

func rewritePythonConstraint(current, version, path, name string) (string, error) {
	if strings.Contains(current, "@") {
		return "", hverrors.New("%s dependency %s has unsupported direct URL syntax", path, name)
	}
	m := pythonConstraintRE.FindStringSubmatch(current)
	if m != nil {
		// String.replace with a string pattern: first textual occurrence.
		return strings.Replace(current, m[2], version, 1), nil
	}
	if idx := strings.Index(current, ";"); idx >= 0 {
		// /\s*;/ anchors at the whitespace run preceding the marker.
		for idx > 0 && unicode.IsSpace(rune(current[idx-1])) {
			idx--
		}
		return current[:idx] + "==" + version + current[idx:], nil
	}
	return current + "==" + version, nil
}

func updatePythonLocalDependencies(path string, owner types.NormalizedPackageConfig, released map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(data))
	found := map[string]bool{}
	changed := false
	section := ""
	inArray := false

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if heading := tomlHeadingRE.FindStringSubmatch(line); heading != nil {
			section = heading[1]
			inArray = false
			continue
		}
		if !isPythonDependencySection(section) {
			continue
		}

		if inArray {
			out, lineChanged, err := rewritePythonRequirementsLine(line, released, found, path)
			if err != nil {
				return err
			}
			lines[index] = out
			changed = changed || lineChanged
			if strings.Contains(lines[index], "]") {
				inArray = false
			}
			continue
		}

		assignment := pythonAssignmentRE.FindStringSubmatch(line)
		if assignment == nil {
			continue
		}
		key := assignment[1]
		value := assignment[2]
		if strings.HasPrefix(value, "[") &&
			(key == "dependencies" || section == "project.optional-dependencies" || strings.HasPrefix(section, "project.optional-dependencies.")) {
			out, lineChanged, err := rewritePythonRequirementsLine(line, released, found, path)
			if err != nil {
				return err
			}
			lines[index] = out
			changed = changed || lineChanged
			if !strings.Contains(value, "]") {
				inArray = true
			}
			continue
		}
		if strings.HasPrefix(section, "tool.poetry") {
			if target, ok := findReleasedName(key, released); ok {
				if len(value) == 0 || (value[0] != '"' && value[0] != '\'') {
					return hverrors.New("%s package %s has unsupported dependency %s", path, owner.Name, key)
				}
				quote := value[0]
				end := strings.IndexByte(value[1:], quote)
				if end < 0 {
					return hverrors.New("%s has malformed dependency %s", path, key)
				}
				end++
				inner := value[1:end]
				next, err := rewritePythonConstraint(inner, released[target], path, key)
				if err != nil {
					return err
				}
				found[target] = true
				if next != inner {
					start := strings.Index(line, value)
					lines[index] = line[:start+1] + next + value[end:]
					changed = true
				}
			}
		} else if key == "dependencies" && strings.HasPrefix(value, "{") {
			for _, name := range sortedKeys(released) {
				if strings.Contains(value, name) {
					return hverrors.New("%s has unsupported inline dependency table", path)
				}
			}
		}
	}

	if err := assertAllDependenciesFound(path, owner.Name, released, found); err != nil {
		return err
	}
	if changed {
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
	}
	return nil
}

// --- rust -------------------------------------------------------------------

var (
	rustWorkspaceTrueRE   = regexp.MustCompile(`workspace\s*=\s*true`)
	rustTargetSectionRE   = regexp.MustCompile(`^target\..+\.(?:dependencies|dev-dependencies|build-dependencies)$`)
	rustDottedRE          = regexp.MustCompile(`^(?:(?:dependencies|dev-dependencies|build-dependencies)|(?:target\..+\.(?:dependencies|dev-dependencies|build-dependencies)))\.((?:"[^"]+"|[A-Za-z0-9_-]+))$`)
	rustWorkspaceDottedRE = regexp.MustCompile(`^workspace\.dependencies\.((?:"[^"]+"|[A-Za-z0-9_-]+))$`)
	rustEntryRE           = regexp.MustCompile(`^\s*(?:"([^"]+)"|([A-Za-z0-9_-]+))\s*=\s*(.*)$`)
	rustInlineTableVerRE  = regexp.MustCompile(`(version\s*=\s*)["'][^"']+["']`)
)

func isRustDependencySection(section string, workspaceOnly bool) bool {
	if workspaceOnly {
		return section == "workspace.dependencies"
	}
	switch section {
	case "dependencies", "dev-dependencies", "build-dependencies":
		return true
	}
	return rustTargetSectionRE.MatchString(section)
}

func findRustDottedDependency(section string, released map[string]string, workspaceOnly bool) (string, bool) {
	re := rustDottedRE
	if workspaceOnly {
		re = rustWorkspaceDottedRE
	}
	m := re.FindStringSubmatch(section)
	if m == nil {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(m[1], `"`), `"`)
	return findReleasedName(name, released)
}

type rustDotted struct {
	target         string
	workspace      bool
	versionUpdated bool
}

type rustActive struct {
	target         string
	depth          int
	workspace      bool
	versionUpdated bool
}

func finishRustDottedDependency(path string, d rustDotted, found map[string]bool) error {
	if !d.workspace && !d.versionUpdated {
		return hverrors.New("%s dependency %s has no supported version field", path, d.target)
	}
	found[d.target] = true
	return nil
}

func braceDelta(value string) int {
	delta := 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '{':
			delta++
		case '}':
			delta--
		}
	}
	return delta
}

func updateRustDependencyTables(path string, owner *types.NormalizedPackageConfig, released map[string]string, workspaceOnly bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(data))
	found := map[string]bool{}
	section := ""
	var active *rustActive
	var dotted *rustDotted
	changed := false

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if active != nil {
			if rustWorkspaceTrueRE.MatchString(line) {
				active.workspace = true
			}
			if !active.workspace && tomlVersionKeyRE.MatchString(line) {
				lines[index] = replaceFirstTomlValue(line, released[active.target])
				active.versionUpdated = true
				changed = true
			}
			active.depth += braceDelta(line)
			if active.depth <= 0 {
				if !active.workspace && !active.versionUpdated {
					return hverrors.New("%s dependency %s has no supported version field", path, active.target)
				}
				found[active.target] = true
				active = nil
			}
			continue
		}

		if heading := tomlHeadingRE.FindStringSubmatch(line); heading != nil {
			if dotted != nil {
				if err := finishRustDottedDependency(path, *dotted, found); err != nil {
					return err
				}
				dotted = nil
			}
			section = heading[1]
			if target, ok := findRustDottedDependency(section, released, workspaceOnly); ok {
				dotted = &rustDotted{target: target}
			}
			continue
		}
		if dotted != nil {
			if rustWorkspaceTrueRE.MatchString(line) {
				dotted.workspace = true
			}
			if !dotted.workspace && tomlVersionKeyRE.MatchString(line) {
				lines[index] = replaceFirstTomlValue(line, released[dotted.target])
				dotted.versionUpdated = true
				changed = true
			}
			continue
		}
		if !isRustDependencySection(section, workspaceOnly) {
			continue
		}

		entry := rustEntryRE.FindStringSubmatch(line)
		if entry == nil {
			continue
		}
		name := entry[1]
		if name == "" {
			name = entry[2]
		}
		target, ok := findReleasedName(name, released)
		if !ok {
			continue
		}
		value := strings.TrimSpace(entry[3])
		if strings.HasPrefix(value, "{") {
			workspace := rustWorkspaceTrueRE.MatchString(value)
			versionMatched := rustInlineTableVerRE.MatchString(value)
			depth := braceDelta(value)
			switch {
			case depth > 0:
				active = &rustActive{target: target, depth: depth, workspace: workspace}
			case workspace:
				found[target] = true
			case versionMatched:
				loc := rustInlineTableVerRE.FindStringSubmatchIndex(line)
				lines[index] = line[:loc[3]] + `"` + released[target] + `"` + line[loc[1]:]
				found[target] = true
				changed = true
			default:
				return hverrors.New("%s dependency %s has unsupported table syntax", path, name)
			}
			continue
		}
		if len(value) > 0 && (value[0] == '"' || value[0] == '\'') {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return hverrors.New("%s has malformed dependency %s", path, name)
			}
			end++
			start := strings.Index(line, value)
			lines[index] = line[:start+1] + released[target] + value[end:]
			found[target] = true
			changed = true
			continue
		}
		return hverrors.New("%s dependency %s has unsupported value", path, name)
	}

	if dotted != nil {
		if err := finishRustDottedDependency(path, *dotted, found); err != nil {
			return err
		}
	}
	if !workspaceOnly && owner != nil {
		if err := assertAllDependenciesFound(path, owner.Name, released, found); err != nil {
			return err
		}
	}
	if changed {
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
	}
	return nil
}

// --- Cargo.lock -------------------------------------------------------------

var (
	lockNameKeyRE   = regexp.MustCompile(`^\s*name\s*=`)
	lockSourceKeyRE = regexp.MustCompile(`^\s*source\s*=`)
	lockDepsStartRE = regexp.MustCompile(`^\s*dependencies\s*=\s*\[`)
	lockParenRE     = regexp.MustCompile(`\s\(`)
)

func replaceLockDependencyEntry(line string, released map[string]string, changed *bool) string {
	return mapQuotedSegments(line, func(content string) (string, bool) {
		if content == "" {
			return content, false
		}
		// Greedy [^"']+ followed by " \d": the LAST space-followed-by-digit
		// boundary splits name from version.
		split := -1
		for k := len(content) - 2; k >= 1; k-- {
			if content[k] == ' ' && content[k+1] >= '0' && content[k+1] <= '9' {
				split = k
				break
			}
		}
		if split < 1 {
			return content, false
		}
		dependencyName := content[:split]
		target, ok := findReleasedName(dependencyName, released)
		if !ok || lockParenRE.MatchString(content) {
			return content, false
		}
		*changed = true
		return `"` + dependencyName + " " + released[target] + `"`, true
	})
}

func updateCargoLock(cwd string, released map[string]string) error {
	path := filepath.Join(cwd, "Cargo.lock")
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return hverrors.New("%s must be a regular file", path)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	var starts []int
	for i, line := range lines {
		if strings.TrimSpace(line) == "[[package]]" {
			starts = append(starts, i)
		}
	}
	changed := false

	for block := 0; block < len(starts); block++ {
		start := starts[block]
		end := len(lines)
		if block+1 < len(starts) {
			end = starts[block+1]
		}
		nameSeen := false
		name := ""
		hasSource := false
		versionIndex := -1
		for i := start; i < end; i++ {
			line := lines[i]
			if !nameSeen && lockNameKeyRE.MatchString(line) {
				nameSeen = true
				name, _ = readTomlString(line, "name")
			}
			if lockSourceKeyRE.MatchString(line) {
				hasSource = true
			}
			if versionIndex < 0 && tomlVersionKeyRE.MatchString(line) {
				versionIndex = i
			}
		}
		target, ok := findReleasedName(name, released)
		if ok && !hasSource {
			if versionIndex < 0 {
				return hverrors.New("%s package %s has no version field", path, name)
			}
			updated := replaceFirstTomlValue(lines[versionIndex], released[target])
			if updated != lines[versionIndex] {
				lines[versionIndex] = updated
				changed = true
			}
		}

		inDependencies := false
		for i := start; i < end; i++ {
			if lockDepsStartRE.MatchString(lines[i]) {
				inDependencies = true
				if strings.Contains(lines[i], "]") {
					inDependencies = false
				}
				continue
			}
			if !inDependencies || hasSource {
				continue
			}
			lines[i] = replaceLockDependencyEntry(lines[i], released, &changed)
			if strings.Contains(lines[i], "]") {
				inDependencies = false
			}
		}
	}

	if changed {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
		if err := writeFileDescriptor(f, strings.Join(lines, "\n")); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func writeFileDescriptor(f *os.File, content string) error {
	data := []byte(content)
	offset := 0
	for offset < len(data) {
		written, err := f.Write(data[offset:])
		if written <= 0 {
			return hverrors.New("Failed to write Cargo.lock")
		}
		if err != nil {
			return err
		}
		offset += written
	}
	return nil
}

// --- TOML [package]/[project] reading ---------------------------------------

func escapeRegExp(value string) string {
	return regexp.QuoteMeta(value)
}

func readPackageJSON(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
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

func readTomlPackage(path, sectionName string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	text := string(data)
	section := getTomlSection(text, sectionName)
	name, _ := readTomlString(section, "name")
	version, _ := readTomlString(section, "version")
	if name == "" || version == "" {
		return "", "", hverrors.New("%s [%s] must contain name and version", path, sectionName)
	}
	return name, version, nil
}

func readVersionFile(path, name string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	version := strings.TrimSpace(string(data))
	if version == "" {
		return "", "", hverrors.New("%s must contain a version", path)
	}
	return name, version, nil
}

func getTomlSection(text, sectionName string) string {
	lines := splitLines(text)
	inSection := false
	var sectionLines []string
	for _, line := range lines {
		heading := tomlHeadingRE.FindStringSubmatch(line)
		if heading != nil {
			if inSection {
				break
			}
			inSection = heading[1] == sectionName
			continue
		}
		if inSection {
			sectionLines = append(sectionLines, line)
		}
	}
	return strings.Join(sectionLines, "\n")
}

func readTomlString(section, key string) (string, bool) {
	re := regexp.MustCompile(`(?m)^\s*` + escapeRegExp(key) + `\s*=\s*["']([^"']+)["']\s*$`)
	m := re.FindStringSubmatch(section)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func updateTomlSectionVersion(path, sectionName, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(data))
	inSection := false
	updated := false

	for i, line := range lines {
		heading := tomlHeadingRE.FindStringSubmatch(line)
		if heading != nil {
			inSection = heading[1] == sectionName
			continue
		}
		if inSection && tomlVersionKeyRE.MatchString(line) {
			updated = true
			lines[i] = replaceFirstTomlValue(line, version)
		}
	}

	if !updated {
		return hverrors.New("%s [%s] does not contain a version field", path, sectionName)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
