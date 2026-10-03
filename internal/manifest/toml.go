package manifest

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/openhoo/hooversion/internal/cargoworkspace"
	hverrors "github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/tomledit"
	"github.com/openhoo/hooversion/internal/types"
)

func loadTOML(path string, files safefs.FileSystem) (*tomledit.Document, error) {
	data, err := files.ReadRegularFile(path, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	d, err := tomledit.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}
func saveTOML(path string, d *tomledit.Document, files safefs.FileSystem) error {
	data, err := d.Render()
	if err != nil {
		return err
	}
	if string(data) == string(d.Data) {
		return nil
	}
	return files.WriteFileAtomic(path, data, 0o644)
}
func readTomlPackageData(path, text, section string) (string, string, error) {
	d, err := tomledit.Parse([]byte(text))
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", path, err)
	}
	name, _ := d.Text(section, "name")
	version, _ := d.Text(section, "version")
	if section == "project" && d.Get("project") == nil {
		name, _ = d.Text("tool", "poetry", "name")
		version, _ = d.Text("tool", "poetry", "version")
	}
	if name == "" || version == "" {
		return "", "", hverrors.New("%s [%s] must contain name and version", path, section)
	}
	return name, version, nil
}

// ReadDataWithWorkspace resolves inherited Rust package versions against bytes
// from the same source revision. It never reads the working tree.
func ReadDataWithWorkspace(pkg types.NormalizedPackageConfig, data, workspaceData []byte) (string, string, error) {
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
	w, err := tomledit.Parse(workspaceData)
	if err != nil {
		return "", "", err
	}
	name, _ := d.Text("package", "name")
	version, _ := w.Text("workspace", "package", "version")
	if name == "" || version == "" {
		return "", "", fmt.Errorf("%s inherited package version requires workspace.package.version", pkg.Manifest)
	}
	return name, version, nil
}

// WorkspaceManifest identifies a workspace root for inherited package metadata.
func WorkspaceManifest(manifest string) (string, error) {
	return workspaceManifestFS(manifest, safefs.Native{})
}

var ErrNoWorkspace = errors.New("no Cargo workspace root")

func workspaceManifestFS(manifest string, files safefs.FileSystem) (string, error) {
	d, err := loadTOML(manifest, files)
	if err != nil {
		return "", err
	}
	if root, ok := d.Text("package", "workspace"); ok {
		path := filepath.Join(filepath.Dir(manifest), root, "Cargo.toml")
		w, err := loadTOML(path, files)
		if err != nil {
			return "", err
		}
		if w.Get("workspace") == nil {
			return "", fmt.Errorf("%s is not a Cargo workspace", path)
		}
		return path, nil
	}
	for dir := filepath.Dir(manifest); ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, "Cargo.toml")
		w, err := loadTOML(path, files)
		if err == nil && w.Get("workspace") != nil {
			return path, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if rooted, ok := files.(*safefs.Root); ok {
			if !filepath.IsAbs(dir) && dir == "." {
				break
			}
			if filepath.Clean(dir) == filepath.Clean(rooted.Path()) {
				break
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return "", fmt.Errorf("%s: %w", manifest, ErrNoWorkspace)
}
func inheritedWorkspace(manifest string, files safefs.FileSystem) (string, bool, error) {
	d, err := loadTOML(manifest, files)
	if err != nil {
		return "", false, err
	}
	if d.Get("package", "version", "workspace") != true {
		return "", false, nil
	}
	path, err := workspaceManifestFS(manifest, files)
	return path, true, err
}
func updateTomlSectionVersion(path, section, version string, files safefs.FileSystem) error {
	d, err := loadTOML(path, files)
	if err != nil {
		return err
	}
	if section == "package" && d.Get("package", "version", "workspace") == true {
		root, err := workspaceManifestFS(path, files)
		if err != nil {
			return err
		}
		return updateTomlSectionVersion(root, "workspace.package", version, files)
	}
	key := []string{section, "version"}
	if section == "project" && d.Get("project") == nil {
		key = []string{"tool", "poetry", "version"}
	}
	if section == "workspace.package" {
		key = []string{"workspace", "package", "version"}
	}
	if err := d.Set(key, version); err != nil {
		return fmt.Errorf("%s [%s] does not contain a version field: %w", path, section, err)
	}
	return saveTOML(path, d, files)
}

// ValidateVersionUpdates rejects changes to shared inherited versions that
// would silently modify an unplanned package or assign conflicting versions.
func ValidateVersionUpdates(cwd string, releases []types.PackageRelease, packages []types.NormalizedPackageConfig) error {
	return ValidateVersionUpdatesWithFS(cwd, releases, packages, safefs.Native{})
}
func ValidateVersionUpdatesWithFS(cwd string, releases []types.PackageRelease, packages []types.NormalizedPackageConfig, files safefs.FileSystem) error {
	next := map[string]string{}
	groups := map[string][]types.NormalizedPackageConfig{}
	for _, r := range releases {
		next[r.Package.Name] = r.NextVersion
	}
	for _, p := range packages {
		if p.Type != types.PackageRust {
			continue
		}
		root, inherited, err := inheritedWorkspace(filepath.Join(cwd, p.Manifest), files)
		if err != nil {
			return err
		}
		if inherited {
			groups[root] = append(groups[root], p)
		}
	}
	for root, group := range groups {
		target := ""
		for _, p := range group {
			v := next[p.Name]
			if v != "" {
				if target != "" && target != v {
					return fmt.Errorf("workspace %s has conflicting inherited version updates", root)
				}
				target = v
			}
		}
		if target == "" {
			continue
		}
		members, err := workspaceMemberManifests(root, files)
		if err != nil {
			return err
		}
		configured := map[string]bool{}
		for _, p := range group {
			configured[filepath.Clean(filepath.Join(cwd, p.Manifest))] = true
		}
		for _, member := range members {
			_, inherited, err := inheritedWorkspace(member, files)
			if err != nil {
				return err
			}
			if inherited && !configured[filepath.Clean(member)] {
				return fmt.Errorf("workspace %s version change affects unconfigured inherited package %s", root, member)
			}
		}
		for _, p := range group {
			if next[p.Name] != target {
				return fmt.Errorf("workspace %s version change also affects package %s; release all inheriting packages together", root, p.Name)
			}
		}
	}
	return nil
}
func ManagedPaths(cwd string, packages []types.NormalizedPackageConfig) ([]string, error) {
	return ManagedPathsWithFS(cwd, packages, safefs.Native{})
}
func ManagedPathsWithFS(cwd string, packages []types.NormalizedPackageConfig, files safefs.FileSystem) ([]string, error) {
	seen := map[string]bool{}
	add := func(path string) error {
		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("manifest mutation escapes repository: %s", path)
		}
		seen[filepath.ToSlash(rel)] = true
		return nil
	}
	for _, p := range packages {
		if err := add(filepath.Join(cwd, p.Manifest)); err != nil {
			return nil, err
		}
		if p.Type != types.PackageRust {
			continue
		}
		root, err := workspaceManifestFS(filepath.Join(cwd, p.Manifest), files)
		if err != nil {
			if !errors.Is(err, ErrNoWorkspace) {
				return nil, err
			}
			d, e := loadTOML(filepath.Join(cwd, p.Manifest), files)
			if e != nil {
				return nil, e
			}
			if d.Get("package", "version", "workspace") == true {
				return nil, err
			}
			root = filepath.Join(cwd, p.Manifest)
		}
		if err := add(root); err != nil {
			return nil, err
		}
		if err := add(filepath.Join(filepath.Dir(root), "Cargo.lock")); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func dependencyTarget(alias string, value any, released map[string]string) (string, bool) {
	if table, ok := value.(map[string]any); ok {
		if name, ok := table["package"].(string); ok {
			return findReleasedName(name, released)
		}
	}
	return findReleasedName(alias, released)
}
func updateRustDependencyTables(path string, owner *types.NormalizedPackageConfig, released map[string]string, workspaceOnly bool, files safefs.FileSystem) error {
	d, err := loadTOML(path, files)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	visit := func(base []string, table map[string]any) error {
		for alias, value := range table {
			target, ok := dependencyTarget(alias, value, released)
			if obj, inherited := value.(map[string]any); inherited && obj["workspace"] == true && !workspaceOnly {
				workspace, err := workspaceManifestFS(path, files)
				if err == nil {
					source, err := loadTOML(workspace, files)
					if err != nil {
						return err
					}
					if inheritedValue := source.Get("workspace", "dependencies", alias); inheritedValue != nil {
						target, ok = dependencyTarget(alias, inheritedValue, released)
					}
				} else if !errors.Is(err, ErrNoWorkspace) {
					return err
				}
			}
			if !ok {
				continue
			}
			key := append(append([]string(nil), base...), alias)
			if obj, ok := value.(map[string]any); ok {
				if obj["workspace"] == true {
					found[target] = true
					continue
				}
				if _, ok := obj["version"].(string); !ok {
					return fmt.Errorf("%s dependency %s has no supported version field", path, alias)
				}
				key = append(key, "version")
			} else if _, ok := value.(string); !ok {
				return fmt.Errorf("%s dependency %s has unsupported value", path, alias)
			}
			if err := d.Set(key, released[target]); err != nil {
				return err
			}
			found[target] = true
		}
		return nil
	}
	var tables func([]string, map[string]any) error
	tables = func(base []string, values map[string]any) error {
		for key, value := range values {
			path := append(append([]string(nil), base...), key)
			obj, ok := value.(map[string]any)
			if !ok {
				continue
			}
			isDependency := key == "dependencies" || key == "dev-dependencies" || key == "build-dependencies"
			if isDependency && ((workspaceOnly && len(base) == 1 && base[0] == "workspace") || (!workspaceOnly && (len(base) == 0 || (len(base) == 2 && base[0] == "target")))) {
				if err := visit(path, obj); err != nil {
					return err
				}
			} else if err := tables(path, obj); err != nil {
				return err
			}
		}
		return nil
	}
	if err := tables(nil, d.Values); err != nil {
		return err
	}
	if owner != nil && !workspaceOnly {
		if err := assertAllDependenciesFound(path, owner.Name, released, found); err != nil {
			return err
		}
	}
	return saveTOML(path, d, files)
}
func updateCargoLock(cwd string, released map[string]string, files safefs.FileSystem) error {
	path := filepath.Join(cwd, "Cargo.lock")
	d, err := loadTOML(path, files)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	packages, _ := d.Get("package").([]any)
	for i, entry := range packages {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		base := []string{"package", fmt.Sprint(i)}
		name, _ := obj["name"].(string)
		_, source := obj["source"]
		if target, ok := findReleasedName(name, released); ok && !source {
			if err := d.Set(append(base, "version"), released[target]); err != nil {
				return err
			}
		}
		if source {
			continue
		}
		deps, _ := obj["dependencies"].([]any)
		for j, value := range deps {
			s, ok := value.(string)
			if !ok {
				continue
			}
			parts := strings.Fields(s)
			if len(parts) != 2 {
				continue
			}
			if target, ok := findReleasedName(parts[0], released); ok {
				if err := d.Set(append(append([]string(nil), base...), "dependencies", fmt.Sprint(j)), parts[0]+" "+released[target]); err != nil {
					return err
				}
			}
		}
	}
	return saveTOML(path, d, files)
}

var pythonNormalizedRE = regexp.MustCompile(`[-_.]+`)

func pythonName(name string) string {
	return pythonNormalizedRE.ReplaceAllString(strings.ToLower(name), "-")
}
func pythonTarget(name string, released map[string]string) (string, bool) {
	for _, key := range sortedKeys(released) {
		if pythonName(name) == pythonName(key) {
			return key, true
		}
	}
	return "", false
}

var pythonRequirementRE = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)(\s*\[[^\]]*\])?\s*(.*)$`)
var pythonSpecifierRE = regexp.MustCompile(`^(===|~=|==|!=|<=|>=|<|>)\s*([0-9][A-Za-z0-9.*+!_-]*(?:\.[A-Za-z0-9*+!_-]+)*)$`)

func splitMarker(s string) (string, string) {
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		if quote != 0 {
			if s[i] == quote {
				quote = 0
			}
			continue
		}
		if s[i] == '\'' || s[i] == '"' {
			quote = s[i]
			continue
		}
		if s[i] == ';' {
			j := i
			for j > 0 && (s[j-1] == ' ' || s[j-1] == '\t') {
				j--
			}
			return s[:j], s[j:]
		}
	}
	return s, ""
}
func rewritePythonConstraint(current, version, path, name string) (string, error) {
	spec, marker := splitMarker(current)
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "@") {
		return "", fmt.Errorf("%s dependency %s has unsupported direct URL syntax", path, name)
	}
	if strings.HasPrefix(spec, "(") && strings.HasSuffix(spec, ")") {
		spec = strings.TrimSpace(spec[1 : len(spec)-1])
	}
	if spec == "" || spec == "*" {
		return "==" + version + marker, nil
	}

	parts := strings.Split(spec, ",")
	var operator string
	for _, part := range parts {
		m := pythonSpecifierRE.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			return "", fmt.Errorf("%s dependency %s has unsupported constraint %q", path, name, spec)
		}
		operator = m[1]
	}
	if len(parts) > 1 {
		return "==" + version + marker, nil
	}
	switch operator {
	case "!=", "<", "<=", ">":
		return "==" + version + marker, nil
	}
	return operator + version + marker, nil
}
func rewritePythonRequirement(requirement, version, path, name string) (string, error) {
	m := pythonRequirementRE.FindStringSubmatch(requirement)
	if m == nil {
		return "", fmt.Errorf("%s invalid requirement %q", path, requirement)
	}
	constraint, err := rewritePythonConstraint(m[3], version, path, name)
	if err != nil {
		return "", err
	}
	return m[1] + m[2] + constraint, nil
}
func poetryConstraint(current, version, path, name string) (string, error) {
	spec, marker := splitMarker(current)
	spec = strings.TrimSpace(spec)
	if strings.Contains(spec, "||") {
		for _, part := range strings.Split(spec, "||") {
			if _, err := poetryConstraint(strings.TrimSpace(part), version, path, name); err != nil {
				return "", err
			}
		}
		return version + marker, nil
	}
	if strings.Contains(spec, ",") {
		for _, part := range strings.Split(spec, ",") {
			if _, err := poetryConstraint(strings.TrimSpace(part), version, path, name); err != nil {
				return "", err
			}
		}
		return version + marker, nil
	}
	prefix := ""
	s := spec
	if strings.HasPrefix(s, "^") {
		prefix = "^"
		s = s[1:]
	} else if strings.HasPrefix(s, "~") && !strings.HasPrefix(s, "~=") {
		prefix = "~"
		s = s[1:]
	}
	if prefix != "" {
		if !regexp.MustCompile(`^[0-9]+(?:\.[0-9*]+)*(?:[A-Za-z0-9+_-]*)$`).MatchString(strings.TrimSpace(s)) {
			return "", fmt.Errorf("%s dependency %s has unsupported Poetry constraint", path, name)
		}
		return prefix + version + marker, nil
	}
	if regexp.MustCompile(`^[0-9]+(?:\.[0-9*]+)*(?:[A-Za-z0-9+_-]*)$`).MatchString(s) || s == "*" {
		return version + marker, nil
	}
	next, err := rewritePythonConstraint(spec, version, path, name)
	if err != nil {
		return "", err
	}
	next = strings.TrimPrefix(next, "==")
	return next + marker, nil
}

func updatePythonLocalDependencies(path string, owner types.NormalizedPackageConfig, released map[string]string, files safefs.FileSystem) error {
	d, err := loadTOML(path, files)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for _, s := range d.Strings {
		p := s.Path
		isRequirement := len(p) == 3 && p[0] == "project" && p[1] == "dependencies" || len(p) == 4 && p[0] == "project" && p[1] == "optional-dependencies" || len(p) == 3 && p[0] == "dependency-groups"
		if isRequirement {
			m := pythonRequirementRE.FindStringSubmatch(s.Value)
			if m == nil {
				continue
			}
			target, ok := pythonTarget(m[1], released)
			if !ok {
				continue
			}
			next, err := rewritePythonRequirement(s.Value, released[target], path, m[1])
			if err != nil {
				return err
			}
			if err := d.SetString(s, next); err != nil {
				return err
			}
			found[target] = true
			continue
		}
		poetry := len(p) >= 4 && p[0] == "tool" && p[1] == "poetry"
		if !poetry {
			continue
		}
		depIndex := -1
		if p[2] == "dependencies" {
			depIndex = 3
		} else if len(p) >= 6 && p[2] == "group" && p[4] == "dependencies" {
			depIndex = 5
		}
		if depIndex < 0 || len(p) <= depIndex {
			continue
		}
		if len(p) > depIndex+1 && !(len(p) == depIndex+2 && p[depIndex+1] == "version") {
			continue
		}
		target, ok := pythonTarget(p[depIndex], released)
		if !ok {
			continue
		}
		if table, ok := d.Get(p[:depIndex+1]...).(map[string]any); ok {
			for _, source := range []string{"url", "git"} {
				if table[source] != nil {
					return fmt.Errorf("%s dependency %s has unsupported direct URL syntax", path, p[depIndex])
				}
			}
		}
		next, err := poetryConstraint(s.Value, released[target], path, p[depIndex])
		if err != nil {
			return err
		}
		if err := d.SetString(s, next); err != nil {
			return err
		}
		found[target] = true
	}
	if err := assertAllDependenciesFound(path, owner.Name, released, found); err != nil {
		return err
	}
	return saveTOML(path, d, files)
}

func dependencyReleasedName(kind types.PackageType, name string, versions map[string]string) (string, bool) {
	if kind == types.PackagePython {
		return pythonTarget(name, versions)
	}
	return findReleasedName(name, versions)
}

func workspaceMemberManifests(root string, files safefs.FileSystem) ([]string, error) {
	d, err := loadTOML(root, files)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(root)
	seen := map[string]bool{}
	excluded := map[string]bool{}
	expand := func(value any) ([]string, error) {
		if value == nil {
			return nil, nil
		}
		array, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("workspace member list must contain strings")
		}
		var out []string
		for _, entry := range array {
			pattern, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("workspace member must be a string")
			}
			matches, err := cargoworkspace.Expand(dir, pattern)
			if err != nil {
				return nil, err
			}
			for _, member := range matches {
				out = append(out, filepath.Join(dir, member, "Cargo.toml"))
			}
		}
		return out, nil
	}
	skips, err := expand(d.Get("workspace", "exclude"))
	if err != nil {
		return nil, err
	}
	for _, p := range skips {
		excluded[p] = true
	}
	members, err := expand(d.Get("workspace", "members"))
	if err != nil {
		return nil, err
	}
	if d.Get("package") != nil {
		members = append(members, root)
	}
	out := []string{}
	for _, p := range members {
		if !excluded[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}
