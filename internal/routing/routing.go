// Package routing assigns commits to releasable packages by scope and by
// touched files. It mirrors src/routing.ts.
package routing

import (
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/types"
)

// DirectAffected maps package name to the commits that directly touch it,
// preserving commit order. A commit reaches a package through a scope match
// (comma-split, case-sensitive, against scopes or the package name itself)
// or through file ownership; each commit is attributed to a package at most
// once. The "." root package owns only files outside every sub-package path.
func DirectAffected(config *types.NormalizedConfig, commits []types.ParsedCommit) map[string][]types.ParsedCommit {
	affected := make(map[string][]types.ParsedCommit)
	for _, commit := range commits {
		names := make(map[string]bool)
		for _, name := range scopeTargets(commit.Scope, config.Packages) {
			names[name] = true
		}
		for _, file := range commit.Files {
			for _, pkg := range config.Packages {
				if fileBelongsToPackage(file, pkg, config.Packages) {
					names[pkg.Name] = true
				}
			}
		}
		for _, pkg := range config.Packages {
			if names[pkg.Name] {
				affected[pkg.Name] = append(affected[pkg.Name], commit)
			}
		}
	}
	return affected
}

// scopeTargets resolves a conventional-commit scope to package names in
// config order; unmatched parts are ignored.
func scopeTargets(scope string, packages []types.NormalizedPackageConfig) []string {
	result := make([]string, 0)
	if scope == "" {
		return result
	}
	seen := make(map[string]bool)
	for _, part := range strings.Split(scope, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, pkg := range packages {
			if containsString(pkg.Scopes, part) || pkg.Name == part {
				if !seen[pkg.Name] {
					seen[pkg.Name] = true
					result = append(result, pkg.Name)
				}
			}
		}
	}
	return result
}

// fileBelongsToPackage mirrors the TS ownership rule: the "." root package
// owns only files that fall outside every other package path.
func fileBelongsToPackage(file string, pkg types.NormalizedPackageConfig, packages []types.NormalizedPackageConfig) bool {
	if pkg.Path == "." {
		for _, other := range packages {
			if other.Path != "." && isInside(file, other.Path) {
				return false
			}
		}
		return true
	}
	return isInside(file, pkg.Path)
}

func isInside(file, packagePath string) bool {
	rel, err := filepath.Rel(packagePath, file)
	if err != nil {
		return false
	}
	return rel == "" || (!strings.HasPrefix(rel, "..") && rel != ".")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
