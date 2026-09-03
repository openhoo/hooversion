// Package config mirrors src/config.ts: config-file discovery (FindPath),
// loading from YAML/JSON (Load), normalization with defaults and validation
// (Normalize), package detection (DetectPackages), default config writing
// (WriteDefault), and legacy TypeScript-config migration (MigrateFromTS).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/manifest"
	"github.com/openhoo/hooversion/internal/types"
	"gopkg.in/yaml.v3"
)

const (
	defaultTagFormat            = "v${version}"
	defaultIndependentTagFormat = "${name}@v${version}"
	defaultOutputDir            = ".hooversion"
	defaultAPIURL               = "https://api.github.com"
)

// yamlConfigFiles and jsonConfigFiles are probed by FindPath in order; the
// legacyConfigFiles are TypeScript-era leftovers that must be migrated.
var (
	yamlConfigFiles = []string{
		"hooversion.yaml",
		".hooversion.yaml",
		"hooversion.yml",
		".hooversion.yml",
	}
	jsonConfigFiles = []string{
		"hooversion.config.json",
		"hooversion.json",
	}
	legacyConfigFiles = []string{
		"hooversion.config.ts",
		"hooversion.config.mjs",
		"hooversion.config.js",
		"hooversion.config.cjs",
	}
)

// LegacyConfigError reports that a legacy TypeScript/JavaScript config file
// exists; callers print the migrate hint from Error() to the user.
type LegacyConfigError struct {
	Path string
}

func (e *LegacyConfigError) Error() string {
	return fmt.Sprintf("Legacy Hooversion config %s detected; run `hooversion migrate` to convert it to hooversion.yaml.", e.Path)
}

// FindPath returns the path of the config file in cwd, probing YAML variants
// first, then JSON variants. A legacy hooversion.config.{ts,mjs,js,cjs}
// yields a *LegacyConfigError so callers can print the migrate hint. When no
// config file exists it returns ("", nil).
func FindPath(cwd string) (string, error) {
	for _, names := range [][]string{yamlConfigFiles, jsonConfigFiles} {
		for _, name := range names {
			path := filepath.Join(cwd, name)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}
	for _, name := range legacyConfigFiles {
		path := filepath.Join(cwd, name)
		if _, err := os.Stat(path); err == nil {
			return "", &LegacyConfigError{Path: path}
		}
	}
	return "", nil
}

// Load reads and normalizes the configuration for cwd. explicitPath (joined to
// cwd) takes precedence over FindPath discovery; JSON and YAML are selected by
// file extension.
func Load(cwd, explicitPath string) (*types.NormalizedConfig, error) {
	configPath := ""
	if explicitPath != "" {
		configPath = filepath.Join(cwd, explicitPath)
	} else {
		found, err := FindPath(cwd)
		if err != nil {
			return nil, err
		}
		if found == "" {
			return nil, errors.New("No hooversion config found. Run `hooversion init` first.")
		}
		configPath = found
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var raw types.Config
	switch ext := filepath.Ext(configPath); ext {
	case ".json":
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, errors.New("Failed to parse config %s: %v", configPath, err)
		}
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, errors.New("Failed to parse config %s: %v", configPath, err)
		}
	default:
		return nil, errors.New("Unsupported config file type: %s", configPath)
	}

	return Normalize(cwd, &raw)
}

// Normalize applies every default and validation rule of src/config.ts:
// required packages, per-package defaults with manifest-read name fallback,
// duplicate-name rejection after trim+lowercase, dependency resolution with
// self-reference rejection, cycle detection, branch ref validation, and
// tag-format validation via placeholder substitution.
func Normalize(cwd string, raw *types.Config) (*types.NormalizedConfig, error) {
	if raw == nil || len(raw.Packages) == 0 {
		return nil, errors.New("Config must define at least one package.")
	}

	packages := make([]types.NormalizedPackageConfig, 0, len(raw.Packages))
	for _, pkg := range raw.Packages {
		normalized, err := normalizePackage(cwd, pkg)
		if err != nil {
			return nil, err
		}
		packages = append(packages, normalized)
	}

	byName := make(map[string]types.NormalizedPackageConfig, len(packages))
	for _, pkg := range packages {
		key := normalizeGraphName(pkg.Name)
		if dup, ok := byName[key]; ok {
			return nil, errors.New("Duplicate package name after normalization: %s and %s", dup.Name, pkg.Name)
		}
		byName[key] = pkg
	}

	graph := make(map[string][]string, len(packages))
	for i := range packages {
		pkg := &packages[i]
		resolved := make([]string, 0, len(pkg.Dependencies))
		for _, dep := range pkg.Dependencies {
			key := normalizeGraphName(dep)
			target, ok := byName[key]
			if !ok {
				return nil, errors.New("Package %s depends on unknown package %s", pkg.Name, dep)
			}
			if key == normalizeGraphName(pkg.Name) {
				return nil, errors.New("Package %s cannot depend on itself", pkg.Name)
			}
			resolved = append(resolved, target.Name)
		}
		pkg.Dependencies = resolved
		edges := make([]string, len(resolved))
		for j, dep := range resolved {
			edges[j] = normalizeGraphName(dep)
		}
		graph[normalizeGraphName(pkg.Name)] = edges
	}
	if err := assertAcyclicPackageGraph(packages, graph); err != nil {
		return nil, err
	}

	branches := raw.Branches
	if len(branches) == 0 {
		branches = []string{"main"}
	}
	tagFormat := raw.TagFormat
	if tagFormat == "" {
		tagFormat = defaultTagFormat
	}
	independentTagFormat := raw.IndependentTagFormat
	if independentTagFormat == "" {
		independentTagFormat = defaultIndependentTagFormat
	}
	for _, branch := range branches {
		if err := git.AssertValidGitRef("branch", branch); err != nil {
			return nil, err
		}
	}
	if err := assertValidTagFormat(tagFormat, packages); err != nil {
		return nil, err
	}
	if err := assertValidTagFormat(independentTagFormat, packages); err != nil {
		return nil, err
	}

	outputDir := raw.OutputDir
	if outputDir != "" {
		var err error
		outputDir, err = normalizeRelative(outputDir)
		if err != nil {
			return nil, err
		}
	}
	normalized := &types.NormalizedConfig{
		Branches:             append([]string(nil), branches...),
		TagFormat:            tagFormat,
		IndependentTagFormat: independentTagFormat,
		Packages:             packages,
		Hooks: types.HookConfig{
			BeforeRelease: orEmptySlice(raw.Hooks.BeforeRelease),
			AfterVersion:  orEmptySlice(raw.Hooks.AfterVersion),
			AfterRelease:  orEmptySlice(raw.Hooks.AfterRelease),
		},
		GitHub:    resolveGitHub(raw.GitHub),
		OutputDir: outputDir,
		Push:      raw.Push == nil || *raw.Push,
	}
	if normalized.OutputDir == "" {
		normalized.OutputDir = defaultOutputDir
	}
	return normalized, nil
}

// resolveGitHub maps the TS `github: false` / `github: {...}` union onto
// GitHubSettings; a disabled section zeroes every other field like TS's bare
// `false`.
func resolveGitHub(raw *types.GitHubConfig) types.GitHubSettings {
	settings := types.GitHubSettings{Enabled: true, Releases: true, ApiUrl: defaultAPIURL}
	if raw == nil {
		return settings
	}
	if raw.Enabled != nil && !*raw.Enabled {
		return types.GitHubSettings{}
	}
	if raw.Releases != nil {
		settings.Releases = *raw.Releases
	}
	settings.Repository = raw.Repository
	if raw.ApiUrl != "" {
		settings.ApiUrl = raw.ApiUrl
	}
	return settings
}

func normalizePackage(cwd string, pkg types.PackageConfig) (types.NormalizedPackageConfig, error) {
	packagePath, err := normalizeRelative(pkg.Path)
	if err != nil {
		return types.NormalizedPackageConfig{}, err
	}
	manifestRel := pkg.Manifest
	if manifestRel == "" {
		manifestRel = DefaultManifestPath(pkg.Type, packagePath)
	}
	manifestRel, err = normalizeRelative(manifestRel)
	if err != nil {
		return types.NormalizedPackageConfig{}, err
	}

	changelog := pkg.Changelog
	if changelog == "" {
		changelog = defaultChangelog(packagePath)
	}
	changelog, err = normalizeRelative(changelog)
	if err != nil {
		return types.NormalizedPackageConfig{}, err
	}

	// internal/manifest.Read receives an absolute manifest path because its
	// locked signature has no cwd parameter; the returned config keeps the
	// cwd-relative form (agreed convention with the manifest package owner).
	infoName, _, err := manifest.Read(types.NormalizedPackageConfig{
		Name:         pkg.Name,
		Path:         packagePath,
		Type:         pkg.Type,
		Manifest:     filepath.Join(cwd, manifestRel),
		Changelog:    changelog,
		Scopes:       orEmptySlice(pkg.Scopes),
		Dependencies: orEmptySlice(pkg.Dependencies),
		Assets:       orEmptySlice(pkg.Assets),
	})
	if err != nil {
		return types.NormalizedPackageConfig{}, err
	}

	name := strings.TrimSpace(pkg.Name)
	if name == "" {
		name = strings.TrimSpace(infoName)
	}

	scopes := []string{name}
	seenScopes := map[string]bool{name: true}
	for _, scope := range pkg.Scopes {
		if seenScopes[scope] {
			continue
		}
		seenScopes[scope] = true
		scopes = append(scopes, scope)
	}

	dependencies := make([]string, 0, len(pkg.Dependencies))
	for _, dep := range pkg.Dependencies {
		dependencies = append(dependencies, strings.TrimSpace(dep))
	}

	return types.NormalizedPackageConfig{
		Name:         name,
		Path:         packagePath,
		Type:         pkg.Type,
		Manifest:     manifestRel,
		Changelog:    changelog,
		Scopes:       scopes,
		Dependencies: dependencies,
		Assets:       orEmptySlice(pkg.Assets),
	}, nil
}

func normalizeGraphName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// assertAcyclicPackageGraph walks dependencies depth-first in package order
// and reports the first cycle as "a -> b -> a".
func assertAcyclicPackageGraph(packages []types.NormalizedPackageConfig, graph map[string][]string) error {
	const (
		stateNone = iota
		stateVisiting
		stateVisited
	)
	state := make(map[string]int)
	var stack []string

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case stateVisited:
			return nil
		case stateVisiting:
			start := 0
			for i, n := range stack {
				if n == name {
					start = i
					break
				}
			}
			cycle := append(append([]string(nil), stack[start:]...), name)
			return errors.New("Package dependency cycle detected: %s", strings.Join(cycle, " -> "))
		}
		state[name] = stateVisiting
		stack = append(stack, name)
		for _, dep := range graph[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = stateVisited
		return nil
	}

	for _, pkg := range packages {
		if err := visit(normalizeGraphName(pkg.Name)); err != nil {
			return err
		}
	}
	return nil
}

// assertValidTagFormat substitutes ${name} per package and ${version} with
// 0.0.0, then validates the candidate through assertValidGitRef semantics.
func assertValidTagFormat(format string, packages []types.NormalizedPackageConfig) error {
	for _, pkg := range packages {
		candidate := strings.ReplaceAll(format, "${name}", pkg.Name)
		candidate = strings.ReplaceAll(candidate, "${version}", "0.0.0")
		if err := git.AssertValidGitRef("tag", candidate); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRelative(rawPath string) (string, error) {
	normalized := strings.ReplaceAll(rawPath, `\`, "/")
	cleaned := pathpkg.Clean(normalized)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
		pathpkg.IsAbs(cleaned) || len(cleaned) >= 2 && cleaned[1] == ':' {
		return "", errors.New("Path must stay inside the repository: %s", rawPath)
	}
	if cleaned == "" {
		return ".", nil
	}
	return cleaned, nil
}

func defaultChangelog(packagePath string) string {
	if packagePath == "." {
		return "CHANGELOG.md"
	}
	return pathpkg.Join(packagePath, "CHANGELOG.md")
}

func orEmptySlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
