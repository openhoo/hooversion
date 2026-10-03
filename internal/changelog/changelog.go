// Package changelog renders release notes and updates CHANGELOG files. It mirrors src/changelog.ts.
package changelog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/openhoo/hooversion/internal/commit"
	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/types"
)

// groupTitles maps a commit class to its notes section title, in output order.
var groupTitles = []struct{ key, title string }{
	{"major", "Breaking Changes"},
	{"feat", "Features"},
	{"fix", "Bug Fixes"},
	{"perf", "Performance"},
}

// GenerateNotes renders release notes for one package version. The date is
// rendered as YYYY-MM-DD in UTC, mirroring toISOString().slice(0, 10).
func GenerateNotes(version string, date time.Time, commits []types.ParsedCommit) string {
	lines := []string{fmt.Sprintf("## %s (%s)", version, date.UTC().Format("2006-01-02")), ""}
	for _, group := range groupCommits(commits) {
		lines = append(lines, "### "+group.title, "")
		for _, c := range group.commits {
			scope := ""
			if c.Scope != "" {
				scope = "**" + c.Scope + ":** "
			}
			lines = append(lines, fmt.Sprintf("- %s%s (%s)", scope, c.Description, hash7(c.Hash)))
			if c.Breaking && c.Body != "" {
				if text, ok := commit.BreakingChange(c.Body); ok {
					lines = append(lines, "  - BREAKING: "+text)
				}
			}
		}
		lines = append(lines, "")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), " \t\n\v\f\r")
}

type commitGroup struct {
	title   string
	commits []types.ParsedCommit
}

func groupCommits(commits []types.ParsedCommit) []commitGroup {
	buckets := map[string][]types.ParsedCommit{}
	var order []string
	push := func(key string, c types.ParsedCommit) {
		if _, seen := buckets[key]; !seen {
			order = append(order, key)
		}
		buckets[key] = append(buckets[key], c)
	}
	for _, c := range commits {
		if c.Breaking {
			push("major", c)
			continue
		}
		matched := false
		for _, g := range groupTitles {
			if g.key == c.Type {
				push(g.key, c)
				matched = true
				break
			}
		}
		if !matched {
			push("Other Changes", c)
		}
	}

	var groups []commitGroup
	for _, g := range groupTitles {
		if cs := buckets[g.key]; len(cs) > 0 {
			groups = append(groups, commitGroup{title: g.title, commits: cs})
		}
	}
	if cs := buckets["Other Changes"]; len(cs) > 0 {
		groups = append(groups, commitGroup{title: "Other Changes", commits: cs})
	}
	return groups
}

func hash7(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// Update prepends the release notes to the changelog at path, creating it
// when missing. Existing content keeps its first "# " header line; otherwise
// a "# <pkgName> Changelog" title is injected. The file is replaced
// atomically: read with O_NOFOLLOW (must be a regular file), written to a
// 0600 O_EXCL temp file that is fsynced and renamed over the target.
func Update(path, notes, pkgName string) error {
	return UpdateWithFS(path, notes, pkgName, safefs.Native{})
}

// UpdateWithFS preserves changelog formatting through a rooted repository writer.
func UpdateWithFS(path, notes, pkgName string, files safefs.FileSystem) error {
	if directories, ok := files.(interface {
		MkdirAll(string, os.FileMode) error
	}); ok {
		if err := directories.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := files.ReadRegularFile(path, 16<<20)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("open %s: %w", path, err)
	}
	next := assemble(string(data), "# "+pkgName+" Changelog", notes)
	return files.WriteFileAtomic(path, []byte(next), 0644)
}

// assemble reproduces the header/body assembly of updateChangelog in
// src/changelog.ts, including its whitespace normalization rules.
func assemble(existing, title, notes string) string {
	normalized := strings.ReplaceAll(existing, "\r\n", "\n")
	if strings.TrimSpace(normalized) == "" {
		normalized = title + "\n"
	}
	first, rest := normalized, ""
	if idx := strings.Index(normalized, "\n"); idx >= 0 {
		first, rest = normalized[:idx], normalized[idx+1:]
	}
	header, body := title, normalized
	if strings.HasPrefix(first, "# ") {
		header, body = first, strings.TrimLeft(rest, "\n")
	}
	return header + "\n\n" + notes + "\n\n" + trimEnd(body) + "\n"
}

func trimEnd(s string) string {
	return strings.TrimRightFunc(s, unicode.IsSpace)
}
