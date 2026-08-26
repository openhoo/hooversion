// Package changelog renders release notes and updates CHANGELOG files. It mirrors src/changelog.ts.
package changelog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/openhoo/hooversion/internal/commit"
	hverrors "github.com/openhoo/hooversion/internal/errors"
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create changelog directory: %w", err)
	}

	existing := ""
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if err != syscall.ENOENT {
			return fmt.Errorf("open %s: %w", path, err)
		}
	} else {
		file := os.NewFile(uintptr(fd), path)
		info, statErr := file.Stat()
		if statErr == nil && !info.Mode().IsRegular() {
			file.Close()
			return hverrors.New("%s must be a regular file", path)
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", path, closeErr)
		}
		existing = string(data)
	}

	title := "# " + pkgName + " Changelog"
	next := assemble(existing, title, notes)

	tempPath, err := writeTemp(path, next)
	if tempPath != "" {
		os.Remove(tempPath) // no-op after successful rename
	}
	return err
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

// writeTemp writes content to "<path>.hooversion-<pid>-<rand>.tmp" and renames
// it over path. It returns the temp path so the caller can clean up after any
// failure; on success the returned path no longer exists.
func writeTemp(path, content string) (string, error) {
	var tempPath string
	for attempt := 0; attempt < 10; attempt++ {
		suffix := make([]byte, 16)
		if _, err := rand.Read(suffix); err != nil {
			return "", fmt.Errorf("generate changelog temp name: %w", err)
		}
		candidate := fmt.Sprintf("%s.hooversion-%d-%s.tmp", path, os.Getpid(), hex.EncodeToString(suffix))
		fd, err := syscall.Open(candidate, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			tempPath = candidate
			if writeErr := writeFile(fd, []byte(content)); writeErr != nil {
				syscall.Close(fd)
				return tempPath, writeErr
			}
			if syncErr := syscall.Fsync(fd); syncErr != nil {
				syscall.Close(fd)
				return tempPath, fmt.Errorf("fsync %s: %w", candidate, syncErr)
			}
			if closeErr := syscall.Close(fd); closeErr != nil {
				return tempPath, fmt.Errorf("close %s: %w", candidate, closeErr)
			}
			if renameErr := syscall.Rename(candidate, path); renameErr != nil {
				return tempPath, fmt.Errorf("rename %s: %w", candidate, renameErr)
			}
			return "", nil
		}
		if err != syscall.EEXIST {
			return "", fmt.Errorf("create %s: %w", candidate, err)
		}
	}
	return tempPath, hverrors.New("Could not create temporary changelog file next to %s", path)
}

func writeFile(fd int, data []byte) error {
	offset := 0
	for offset < len(data) {
		written, err := syscall.Write(fd, data[offset:])
		if err != nil {
			return hverrors.New("Failed to write changelog")
		}
		if written <= 0 {
			return hverrors.New("Failed to write changelog")
		}
		offset += written
	}
	return nil
}
