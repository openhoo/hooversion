// Package cargoworkspace expands Cargo workspace directory patterns without
// following symbolic links or permitting paths outside the workspace root.
package cargoworkspace

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/safefs"
)

func Expand(root, pattern string) ([]string, error) {
	pattern = filepath.ToSlash(pattern)
	if !filepath.IsLocal(filepath.FromSlash(pattern)) {
		return nil, fmt.Errorf("workspace member escapes root: %q", pattern)
	}
	pattern = path.Clean(pattern)
	if len(strings.Split(pattern, "/")) > 256 {
		return nil, fmt.Errorf("workspace glob exceeds 256 path components")
	}
	// Validate each glob component before walking, even if no candidates exist.
	for _, component := range strings.Split(pattern, "/") {
		if component == "**" {
			continue
		}
		if _, err := path.Match(component, ""); err != nil {
			return nil, err
		}
	}
	var matches []string
	if strings.Contains(pattern, "**") {
		visited := 0
		err := filepath.WalkDir(root, func(candidate string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			visited++
			if visited > 100000 {
				return fmt.Errorf("workspace glob inspection exceeds 100000 entries")
			}
			rel, err := filepath.Rel(root, candidate)
			if err != nil {
				return err
			}
			if match(strings.Split(pattern, "/"), strings.Split(filepath.ToSlash(rel), "/")) {
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					matches = append(matches, candidate)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		matches, err = filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(matches))
	for _, candidate := range matches {
		rel, err := filepath.Rel(root, candidate)
		if err != nil {
			return nil, err
		}
		if err := safefs.RequireContainedPath(root, filepath.Join(rel, "Cargo.toml")); err != nil {
			return nil, err
		}
		info, err := os.Lstat(candidate)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("cargo workspace member %s is not a directory", rel)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}
func match(pattern, parts []string) bool {
	type state struct{ i, j int }
	memo := map[state]bool{}
	known := map[state]bool{}
	var visit func(int, int) bool
	visit = func(i, j int) bool {
		k := state{i, j}
		if known[k] {
			return memo[k]
		}
		known[k] = true
		result := false
		if i == len(pattern) {
			result = j == len(parts)
		} else if pattern[i] == "**" {
			result = visit(i+1, j) || (j < len(parts) && visit(i, j+1))
		} else if j < len(parts) {
			ok, _ := path.Match(pattern[i], parts[j])
			result = ok && visit(i+1, j+1)
		}
		memo[k] = result
		return result
	}
	return visit(0, 0)
}
