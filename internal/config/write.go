// Default config writing; mirrors writeDefaultConfig data of src/config.ts
// rendered as hooversion.yaml instead of a TypeScript module.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/types"
)

// WriteDefault detects packages in cwd and writes a hooversion.yaml carrying
// the same defaults as writeDefaultConfig: branches ["main"],
// hooks.afterVersion [], github.releases true.
func WriteDefault(cwd string) (string, error) {
	pkgs, err := DetectPackages(cwd)
	if err != nil {
		return "", err
	}
	if len(pkgs) == 0 {
		return "", errors.New("Could not detect package.json, Cargo.toml, pyproject.toml, or version.")
	}

	cfg, err := Normalize(cwd, &types.Config{Packages: pkgs})
	if err != nil {
		return "", err
	}

	body := renderYAML(cfg)
	path := filepath.Join(cwd, "hooversion.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// renderYAML serializes a normalized config into camelCase YAML matching the
// keys of types.Config so the result parses back through Load. Strings are
// always double-quoted for unambiguous YAML scalars.
func renderYAML(cfg *types.NormalizedConfig) []byte {
	var b strings.Builder

	b.WriteString("branches:\n")
	for _, branch := range cfg.Branches {
		fmt.Fprintf(&b, "  - %q\n", branch)
	}
	fmt.Fprintf(&b, "tagFormat: %q\n", cfg.TagFormat)
	fmt.Fprintf(&b, "independentTagFormat: %q\n", cfg.IndependentTagFormat)

	b.WriteString("packages:\n")
	for _, pkg := range cfg.Packages {
		fmt.Fprintf(&b, "  - name: %q\n", pkg.Name)
		fmt.Fprintf(&b, "    path: %q\n", pkg.Path)
		fmt.Fprintf(&b, "    type: %q\n", string(pkg.Type))
		fmt.Fprintf(&b, "    manifest: %q\n", pkg.Manifest)
		fmt.Fprintf(&b, "    changelog: %q\n", pkg.Changelog)
		writeStringList(&b, "    scopes", pkg.Scopes)
		writeStringList(&b, "    dependencies", pkg.Dependencies)
		writeStringList(&b, "    assets", pkg.Assets)
	}

	hooks := cfg.Hooks
	b.WriteString("hooks:\n")
	writeStringList(&b, "  beforeRelease", hooks.BeforeRelease)
	writeStringList(&b, "  afterVersion", hooks.AfterVersion)
	writeStringList(&b, "  afterRelease", hooks.AfterRelease)

	if cfg.GitHub.Enabled {
		b.WriteString("github:\n")
		b.WriteString("  enabled: true\n")
		fmt.Fprintf(&b, "  releases: %t\n", cfg.GitHub.Releases)
		if cfg.GitHub.Repository != "" {
			fmt.Fprintf(&b, "  repository: %q\n", cfg.GitHub.Repository)
		}
		if cfg.GitHub.ApiUrl != "" {
			fmt.Fprintf(&b, "  apiUrl: %q\n", cfg.GitHub.ApiUrl)
		}
	} else {
		b.WriteString("github: false\n")
	}

	fmt.Fprintf(&b, "outputDir: %q\n", cfg.OutputDir)
	fmt.Fprintf(&b, "push: %t\n", cfg.Push)
	return []byte(b.String())
}

func writeStringList(b *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		fmt.Fprintf(b, "%s: []\n", key)
		return
	}
	fmt.Fprintf(b, "%s:\n", key)
	indent := key[:len(key)-len(strings.TrimLeft(key, " "))] + "  "
	for _, v := range values {
		fmt.Fprintf(b, "%s- %q\n", indent, v)
	}
}
