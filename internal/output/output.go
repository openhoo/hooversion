// Package output manages the release payload files under the configured
// output directory, including GitHub Actions outputs. It mirrors src/output.ts.
package output

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/types"
)

// ManagedPaths is the clean-working-tree exemption set: an entry maps an
// absolute path to false when it is one exact managed file, or to true when
// it is the managed output directory whose gitignored contents must never
// hide stale release payloads.
type ManagedPaths map[string]bool

// Store reads and clears the managed release payload for one repository
// checkout. OutputDir is the cwd-relative configured directory.
type Store struct {
	Cwd       string
	OutputDir string
}

type stalePayload struct {
	Releases []struct {
		Tag       any    `json:"tag"`
		NotesPath string `json:"notesPath"`
	} `json:"releases"`
}

// Paths returns every managed path: outputs.json, .release-version, the
// per-tag note names inferred from a stale payload (advisory), legacy
// notesPath names that remain inside the output directory, and the output
// directory itself.
func (s Store) Paths() ManagedPaths {
	outputsPath := s.outputsPath()
	releaseVersionPath := filepath.Join(s.Cwd, ".release-version")
	paths := ManagedPaths{outputsPath: false, releaseVersionPath: false}

	dir := filepath.Join(s.Cwd, s.OutputDir)
	if dir != "" {
		paths[dir] = true
	}

	payload, ok := readStalePayload(outputsPath)
	if !ok {
		return paths
	}
	for _, release := range payload.Releases {
		if notePath, ok := legacyNotePath(s.Cwd, dir, release.NotesPath); ok {
			paths[notePath] = false
		}
	}

	tags := make([]string, 0, len(payload.Releases))
	for _, release := range payload.Releases {
		if tag, ok := release.Tag.(string); ok {
			tags = append(tags, tag)
		}
	}
	noteNames, err := deriveNoteNames(tags)
	if err != nil {
		return paths
	}
	tagIndex := 0
	for _, release := range payload.Releases {
		_, ok := release.Tag.(string)
		if !ok {
			continue
		}
		noteName := noteNames[tagIndex]
		tagIndex++
		if notePath, ok := containedPath(dir, filepath.Join(dir, noteName)); ok {
			paths[notePath] = false
		}
	}
	return paths
}

// legacyNotePath resolves a stored notesPath against the checkout and admits
// it only when it is a non-root descendant of the configured output directory.
// Old payloads are advisory input, so paths outside that directory are ignored.
func legacyNotePath(cwd, outputDir, notesPath string) (string, bool) {
	if notesPath == "" {
		return "", false
	}
	path := notesPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	candidate, ok := containedPath(outputDir, path)
	if !ok || hasSymlinkParent(outputDir, candidate) {
		return "", false
	}
	return candidate, true
}

// containedPath returns candidate only when it is strictly contained by root.
// Absolute paths are used for the comparison so relative checkout paths cannot
// bypass the boundary with traversal segments.
func containedPath(root, candidate string) (string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil || rel == "" || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.Clean(candidate), true
}

// hasSymlinkParent rejects a legacy note whose parent component would make
// os.Remove follow a symlink outside the output directory. A missing parent
// is safe because Clear skips a missing candidate.
func hasSymlinkParent(root, candidate string) bool {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		if !os.IsNotExist(err) {
			return true
		}
	} else if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return true
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return true
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return true
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil || rel == "." || rel == "" {
		return true
	}
	parts := strings.Split(rel, string(filepath.Separator))
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return !os.IsNotExist(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return true
		}
	}
	return false
}

// readStalePayload parses outputs.json without following a symlink planted at
// the path; any failure means the stale output cannot identify note paths.
func readStalePayload(path string) (stalePayload, bool) {
	file, err := safefs.OpenReadNoFollow(path)
	if err != nil {
		return stalePayload{}, false
	}
	defer file.Close()

	var payload stalePayload
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return payload, false
	}
	data := make([]byte, info.Size())
	read := 0
	for read < len(data) {
		n, err := file.Read(data[read:])
		read += n
		if err != nil {
			break
		}
	}
	if err := json.Unmarshal(data[:read], &payload); err != nil {
		return stalePayload{}, false
	}
	return payload, true
}

// Clear removes the managed payload files. Missing paths are already clean;
// anything else (including a directory planted at a managed path) fails.
// The advisory stale-payload parse never follows symlinks, so a symlinked
// outputs.json is unlinked itself and its target survives.
func (s Store) Clear() error {
	for path, dirScope := range s.Paths() {
		if dirScope {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("cannot clear managed output path %s: it is a directory", path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

type storedRelease struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Tag       string `json:"tag"`
	Type      string `json:"type"`
	NotesPath string `json:"notesPath"`
}

type storedPayload struct {
	Published bool            `json:"published"`
	Releases  []storedRelease `json:"releases"`
}

// Write replaces the whole payload atomically in effect: clear, then write
// per-tag notes, outputs.json, .release-version (single release only), and
// append GITHUB_OUTPUT lines when that variable is set. published reports
// whether the pipeline actually published; it is combined with the release
// count exactly like src/output.ts derives it.
func (s Store) Write(releases []types.PackageRelease, published bool) error {
	tags := make([]string, len(releases))
	for i, release := range releases {
		tags[i] = release.Tag
	}
	noteNames, err := deriveNoteNames(tags)
	if err != nil {
		return err
	}
	if err := s.Clear(); err != nil {
		return err
	}
	dir := filepath.Join(s.Cwd, s.OutputDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	stored := make([]storedRelease, 0, len(releases))
	for i, release := range releases {
		noteName := noteNames[i]
		if err := os.WriteFile(filepath.Join(dir, noteName), []byte(release.Notes+"\n"), 0o644); err != nil {
			return err
		}
		stored = append(stored, storedRelease{
			Name:      release.Package.Name,
			Version:   release.NextVersion,
			Tag:       release.Tag,
			Type:      string(release.ReleaseType),
			NotesPath: fmt.Sprintf("%s/%s", s.OutputDir, noteName),
		})
	}

	payloadPublished := published && len(releases) > 0
	data, err := marshalJSONIndent(storedPayload{Published: payloadPublished, Releases: stored})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "outputs.json"), data, 0o644); err != nil {
		return err
	}

	versionPath := filepath.Join(s.Cwd, ".release-version")
	if len(releases) == 1 {
		if err := os.WriteFile(versionPath, []byte(releases[0].NextVersion+"\n"), 0o644); err != nil {
			return err
		}
	} else if err := removeIfExists(versionPath); err != nil {
		return err
	}

	if githubOutput := os.Getenv("GITHUB_OUTPUT"); githubOutput != "" {
		lines := []string{
			fmt.Sprintf("published=%t", payloadPublished),
			"releases_json=" + marshalJSONCompact(stored),
		}
		if len(releases) == 1 {
			lines = append(lines,
				"version="+releases[0].NextVersion,
				"tag="+releases[0].Tag,
			)
		}
		file, err := os.OpenFile(githubOutput, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// marshalJSONIndent renders 2-space JSON with a trailing newline and no HTML
// escaping, matching JavaScript JSON.stringify(value, null, 2).
func marshalJSONIndent(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // Encode already appends exactly one "\n".
}

func marshalJSONCompact(value any) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "null"
	}
	return strings.TrimRight(buf.String(), "\n")
}

const (
	maxNoteFileNameBytes = 255
	noteFileSuffix       = "-notes.md"
)

func deriveNoteNames(tags []string) ([]string, error) {
	baseCounts := make(map[string]int, len(tags))
	for _, tag := range tags {
		baseCounts[sanitizeFileName(tag)]++
	}
	const hashHexBytes = sha256.Size * 2
	hashSuffixBytes := 1 + hashHexBytes + len(noteFileSuffix)
	maxReadableBaseBytes := maxNoteFileNameBytes - hashSuffixBytes
	names := make([]string, len(tags))
	seen := make(map[string]string, len(tags))
	for i, tag := range tags {
		base := sanitizeFileName(tag)
		needsHash := baseCounts[base] > 1 || len(base)+len(noteFileSuffix) > maxNoteFileNameBytes
		name := base
		if needsHash {
			sum := sha256.Sum256([]byte(tag))
			if len(name) > maxReadableBaseBytes {
				name = name[:maxReadableBaseBytes]
			}
			name += "-" + hex.EncodeToString(sum[:])
		}
		name += noteFileSuffix
		if previous, exists := seen[name]; exists {
			return nil, fmt.Errorf("release note path collision for tags %q and %q", previous, tag)
		}
		seen[name] = tag
		names[i] = name
	}
	return names, nil
}

func sanitizeFileName(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '@', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	return builder.String()
}

func (s Store) outputsPath() string {
	return filepath.Join(s.Cwd, s.OutputDir, "outputs.json")
}
