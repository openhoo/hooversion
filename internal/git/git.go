// Package git wraps the git CLI surface used by Hooversion.
// It mirrors src/git.ts 1:1, including argv shapes, allow-failure semantics,
// env merging for network commands, and exact user-facing error strings.
package git

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	hverrors "github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/types"
)

// ErrNoRemote reports that no remote.origin.url is configured. It mirrors the
// TS `undefined` return of getRemoteBranchSha for the no-remote case.
var ErrNoRemote = errors.New("git: no remote.origin.url configured")

// ErrNoTag reports that `git describe --tags --match <pattern>` found no tag.
// It mirrors the TS `undefined` return of getLatestTag.
var ErrNoTag = errors.New("git: no tag found")

// jsQuote renders s like JavaScript JSON.stringify (compact, no HTML escaping),
// so error strings match the TS implementation byte-for-byte.
func jsQuote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return ""
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// commandResult mirrors the TS runCommand result shape.
type commandResult struct {
	stdout string
	stderr string
	code   int
}

func runCommand(cwd string, env []string, args ...string) commandResult {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := commandResult{stdout: stdout.String(), stderr: stderr.String(), code: 0}
	if err == nil {
		return res
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.code = exitErr.ExitCode()
	} else {
		res.code = 1
		res.stderr = err.Error()
	}
	return res
}

// childEnv merges auth variables over base. A nil base preserves ordinary CLI
// inheritance; an explicit base is copied so release execution is isolated.
func childEnvWithBase(base []string, auth types.GitAuth) []string {
	if base == nil {
		return childEnv(auth)
	}
	env := append([]string{}, base...)
	for k, v := range auth {
		env = append(env, k+"="+v)
	}
	return env
}

func childEnv(auth types.GitAuth) []string {
	if len(auth) == 0 {
		return nil
	}
	env := append([]string{}, os.Environ()...)
	for k, v := range auth {
		env = append(env, k+"="+v)
	}
	return env
}

func gitRunWithBase(cwd string, args []string, allowFailure bool, auth types.GitAuth, base []string) (string, error) {
	res := runCommand(cwd, childEnvWithBase(base, auth), args...)
	if res.code != 0 && !allowFailure {
		detail := res.stderr
		if detail == "" {
			detail = res.stdout
		}
		return "", hverrors.New("git %s failed:\n%s", strings.Join(args, " "), detail)
	}
	return trimEnd(res.stdout), nil
}

func trimEnd(s string) string {
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

func gitRun(cwd string, args []string, allowFailure bool, auth types.GitAuth) (string, error) {
	res := runCommand(cwd, childEnv(auth), args...)
	if res.code != 0 && !allowFailure {
		detail := res.stderr
		if detail == "" {
			detail = res.stdout
		}
		return "", hverrors.New("git %s failed:\n%s", strings.Join(args, " "), detail)
	}
	return trimEnd(res.stdout), nil
}

func hasForbiddenRefRune(s string) bool {
	const literals = "~^:?*\\"
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune(literals, r) {
			return true
		}
	}
	return false
}

// AssertValidGitRef validates a short branch or tag name before it is
// interpolated into a Git command. It mirrors the security-relevant subset of
// check-ref-format and additionally rejects option-like names.
func AssertValidGitRef(kind, name string) error {
	invalid := name == "" ||
		name == "@" ||
		strings.HasPrefix(name, "-") ||
		strings.HasPrefix(name, "refs/") ||
		strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") ||
		strings.Contains(name, "//") ||
		strings.Contains(name, "..") ||
		strings.Contains(name, "@{") ||
		hasForbiddenRefRune(name) ||
		strings.Contains(name, "[")
	if !invalid {
		for _, component := range strings.Split(name, "/") {
			if component == "" || component == "." || component == ".." ||
				strings.HasPrefix(component, ".") ||
				strings.HasSuffix(component, ".") ||
				strings.HasSuffix(strings.ToLower(component), ".lock") {
				invalid = true
				break
			}
		}
	}
	if invalid {
		return hverrors.New("Invalid Git %s name: %s", kind, jsQuote(name))
	}
	return nil
}

// IsGitRepository reports whether cwd is inside a git work tree.
func IsGitRepository(cwd string) bool {
	return runCommand(cwd, nil, "rev-parse", "--is-inside-work-tree").code == 0
}

// CurrentBranch resolves the checked-out branch: `git branch --show-current`,
// falling back to CI env vars and finally `rev-parse --abbrev-ref HEAD`.
func CurrentBranch(cwd string) (string, error) {
	branch, err := gitRun(cwd, []string{"branch", "--show-current"}, false, nil)
	if err != nil {
		return "", err
	}
	branch = strings.TrimSpace(branch)
	if branch != "" {
		return branch, nil
	}
	if v := os.Getenv("GITHUB_HEAD_REF"); v != "" {
		return v, nil
	}
	if os.Getenv("GITHUB_REF_TYPE") != "tag" {
		if v := os.Getenv("GITHUB_REF_NAME"); v != "" {
			return v, nil
		}
	}
	out, err := gitRun(cwd, []string{"rev-parse", "--abbrev-ref", "HEAD"}, false, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// EnsureCleanWorkingTree fails while the working tree carries dirt. Managed
// paths stay ignorable: managed[path]=false exempts that exact path;
// managed[dir]=true additionally treats gitignored files *inside* that
// directory as unexpected (stale release payloads must never hide there).
func EnsureCleanWorkingTree(cwd string, managed map[string]bool) error {
	exempt := make(map[string]bool, len(managed))
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(cwd, p)
	}
	for p := range managed {
		exempt[abs(p)] = true
	}

	statusOut, err := gitRun(cwd, []string{"status", "--porcelain", "--untracked-files=all"}, false, nil)
	if err != nil {
		return err
	}
	var unexpected []string
	for _, line := range strings.Split(statusOut, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entry := line
		if len(line) > 3 {
			entry = strings.TrimSpace(line[3:])
		}
		if !exempt[abs(entry)] {
			unexpected = append(unexpected, line)
		}
	}

	for dir, scoped := range managed {
		if !scoped {
			continue
		}
		rel := dir
		if filepath.IsAbs(dir) {
			rel, err = filepath.Rel(cwd, dir)
			if err != nil {
				continue
			}
		}
		if rel == "." || rel == ".." || rel == "" || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		ignoredOut, err := gitRun(cwd, []string{
			"ls-files", "--others", "--ignored", "--exclude-standard", "--", rel,
		}, true, nil)
		if err != nil {
			continue
		}
		for _, p := range strings.Split(ignoredOut, "\n") {
			p = strings.TrimSpace(p)
			if p == "" || exempt[abs(p)] {
				continue
			}
			unexpected = append(unexpected, "?? "+p)
		}
	}

	if len(unexpected) > 0 {
		return hverrors.New("Working tree must be clean before release:\n%s", strings.Join(unexpected, "\n"))
	}
	return nil
}

// LatestTag returns the most recent tag matching pattern (used with a
// describe --match glob derived from the tag format), or ErrNoTag.
func LatestTag(cwd, pattern string) (string, error) {
	output, err := gitRun(cwd, []string{"describe", "--tags", "--abbrev=0", "--match", pattern}, true, nil)
	if err != nil {
		return "", err
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return "", ErrNoTag
	}
	return output, nil
}

// TagExists reports whether refs/tags/<tag> resolves.
func TagExists(cwd, tag string) (bool, error) {
	if err := AssertValidGitRef("tag", tag); err != nil {
		return false, err
	}
	// Note: unlike the TS source we omit the bare `--` before the ref; with
	// current git it demotes the ref to a pathspec and --verify always fails.
	return runCommand(cwd, nil, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag).code == 0, nil
}

// HeadSha returns the full SHA of HEAD.
func HeadSha(cwd string) (string, error) {
	out, err := gitRun(cwd, []string{"rev-parse", "HEAD"}, false, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func isFullSha(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// RefSha resolves a whitelisted ref to a commit SHA: HEAD, HEAD^, a 40-hex
// SHA, refs/tags/<tag>^{commit} (annotated tags peel to the commit), or
// refs/heads/<branch>. Anything else is rejected. A missing ref yields "" with
// a nil error, mirroring the TS `undefined`.
func RefSha(cwd, ref string) (string, error) {
	var commitRef string
	switch {
	case ref == "HEAD" || ref == "HEAD^" || isFullSha(ref):
		commitRef = ref
	case strings.HasPrefix(ref, "refs/tags/"):
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if err := AssertValidGitRef("tag", tag); err != nil {
			return "", err
		}
		commitRef = ref + "^{commit}"
	case strings.HasPrefix(ref, "refs/heads/"):
		if err := AssertValidGitRef("branch", strings.TrimPrefix(ref, "refs/heads/")); err != nil {
			return "", err
		}
		commitRef = ref
	default:
		return "", hverrors.New("Invalid Git revision: %s", jsQuote(ref))
	}
	res := runCommand(cwd, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", commitRef)
	if res.code != 0 {
		return "", nil
	}
	return strings.TrimSpace(res.stdout), nil
}

// RemoteBranchSha looks up refs/heads/<branch> on origin via ls-remote.
// Results mirror the TS tri-state: ErrNoRemote when no remote.origin.url is
// configured, "" when the branch is missing remotely, otherwise the SHA.
func RemoteBranchSha(cwd, branch string) (string, error) {
	if err := AssertValidGitRef("branch", branch); err != nil {
		return "", err
	}
	remote, err := gitRun(cwd, []string{"config", "--get", "remote.origin.url"}, true, nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(remote) == "" {
		return "", ErrNoRemote
	}
	output, err := gitRun(cwd, []string{"ls-remote", "--", "origin", "refs/heads/" + branch}, true, nil)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// CommitMessage returns the full message (%B) of ref, trailing newline
// stripped.
func CommitMessage(cwd, ref string) (string, error) {
	return gitRun(cwd, []string{"show", "-s", "--format=%B", ref}, false, nil)
}

// LastCommit collects the newest commit as a RawCommit.
func LastCommit(cwd string) (types.RawCommit, error) {
	hash, err := HeadSha(cwd)
	if err != nil {
		return types.RawCommit{}, err
	}
	subject, err := gitRun(cwd, []string{"show", "-s", "--format=%s", hash}, false, nil)
	if err != nil {
		return types.RawCommit{}, err
	}
	body, err := gitRun(cwd, []string{"show", "-s", "--format=%b", hash}, false, nil)
	if err != nil {
		return types.RawCommit{}, err
	}
	filesOut, err := gitRun(cwd, []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", hash}, true, nil)
	if err != nil {
		return types.RawCommit{}, err
	}
	files := []string{}
	for _, f := range strings.Split(filesOut, "\n") {
		f = strings.TrimSpace(f)
		if f != "" {
			files = append(files, f)
		}
	}
	return types.RawCommit{Hash: hash, Subject: subject, Body: body, Files: files}, nil
}

// PushRelease pushes the release branch head plus every release tag in ONE
// atomic, hook-skipping `git push`. Nothing lands partially.
func PushRelease(cwd, branch string, tags []string, auth types.GitAuth) error {
	if err := AssertValidGitRef("branch", branch); err != nil {
		return err
	}
	for _, tag := range tags {
		if err := AssertValidGitRef("tag", tag); err != nil {
			return err
		}
	}
	args := []string{"push", "--atomic", "--no-verify", "--", "origin", "HEAD:refs/heads/" + branch}
	for _, tag := range tags {
		args = append(args, "refs/tags/"+tag)
	}
	_, err := gitRun(cwd, args, false, auth)
	return err
}

// Commits walks rev-list --reverse over from..to (whole history when from is
// empty) and collects subject, body, and touched files per commit.
func Commits(cwd, from, to string) ([]types.RawCommit, error) {
	rangeArg := to
	if from != "" {
		rangeArg = from + ".." + to
	}
	revList, err := gitRun(cwd, []string{"rev-list", "--reverse", rangeArg}, true, nil)
	if err != nil {
		return nil, err
	}
	if revList == "" {
		return []types.RawCommit{}, nil
	}
	commits := []types.RawCommit{}
	for _, hash := range strings.Split(revList, "\n") {
		subject, err := gitRun(cwd, []string{"show", "-s", "--format=%s", hash}, false, nil)
		if err != nil {
			return nil, err
		}
		body, err := gitRun(cwd, []string{"show", "-s", "--format=%b", hash}, false, nil)
		if err != nil {
			return nil, err
		}
		filesOut, err := gitRun(cwd, []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", hash}, true, nil)
		if err != nil {
			return nil, err
		}
		files := []string{}
		for _, f := range strings.Split(filesOut, "\n") {
			f = strings.TrimSpace(f)
			if f != "" {
				files = append(files, f)
			}
		}
		commits = append(commits, types.RawCommit{Hash: hash, Subject: subject, Body: body, Files: files})
	}
	return commits, nil
}

// CreateReleaseCommit stages everything and commits with message, skipping
// the commit when nothing changed.
func CreateReleaseCommit(cwd, message string) error {
	if _, err := gitRun(cwd, []string{"add", "--all"}, false, nil); err != nil {
		return err
	}
	status, err := gitRun(cwd, []string{"status", "--porcelain"}, false, nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	_, err = gitRun(cwd, []string{"commit", "-m", message}, false, nil)
	return err
}

// CreateAnnotatedTag creates tag with message via `git tag -a`.
func CreateAnnotatedTag(cwd, tag, message string) error {
	if err := AssertValidGitRef("tag", tag); err != nil {
		return err
	}
	_, err := gitRun(cwd, []string{"tag", "-a", tag, "-m", message}, false, nil)
	return err
}

var (
	sshRepoRE   = regexp.MustCompile(`^git@[^:]+:([^/]+/[^/.]+)(?:\.git)?$`)
	httpsRepoRE = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/.]+)(?:\.git)?$`)
)

// OriginRepository parses remote.origin.url into "owner/repo"; "" when no
// origin exists or the URL shape is unrecognized (mirrors TS `undefined`).
func OriginRepository(cwd string) (string, error) {
	remote, err := gitRun(cwd, []string{"config", "--get", "remote.origin.url"}, true, nil)
	if err != nil {
		return "", err
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", nil
	}
	if m := sshRepoRE.FindStringSubmatch(remote); m != nil {
		return m[1], nil
	}
	if m := httpsRepoRE.FindStringSubmatch(remote); m != nil {
		return m[1], nil
	}
	return "", nil
}

// HeadShaWithEnv is HeadSha with an explicit child environment.
func HeadShaWithEnv(cwd string, baseEnv []string) (string, error) {
	out, err := gitRunWithBase(cwd, []string{"rev-parse", "HEAD"}, false, nil, baseEnv)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RefShaWithEnv resolves a whitelisted revision with an explicit environment.
func RefShaWithEnv(cwd, ref string, baseEnv []string) (string, error) {
	var commitRef string
	switch {
	case ref == "HEAD" || ref == "HEAD^" || isFullSha(ref):
		commitRef = ref
	case strings.HasPrefix(ref, "refs/tags/"):
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if err := AssertValidGitRef("tag", tag); err != nil {
			return "", err
		}
		commitRef = ref + "^{commit}"
	case strings.HasPrefix(ref, "refs/heads/"):
		if err := AssertValidGitRef("branch", strings.TrimPrefix(ref, "refs/heads/")); err != nil {
			return "", err
		}
		commitRef = ref
	default:
		return "", hverrors.New("Invalid Git revision: %s", jsQuote(ref))
	}
	res := runCommand(cwd, childEnvWithBase(baseEnv, nil), "rev-parse", "--verify", "--quiet", "--end-of-options", commitRef)
	if res.code != 0 {
		return "", nil
	}
	return strings.TrimSpace(res.stdout), nil
}

// RemoteTagSha resolves a remote tag, peeling annotated tags to their commit.
// It returns ErrNoRemote when origin is not configured and an empty SHA when
// the tag is absent.
func RemoteTagSha(cwd, tag string) (string, error) {
	return RemoteTagShaWithEnv(cwd, tag, nil)
}

func RemoteTagShaWithEnv(cwd, tag string, baseEnv []string) (string, error) {
	if err := AssertValidGitRef("tag", tag); err != nil {
		return "", err
	}
	remote, err := gitRunWithBase(cwd, []string{"config", "--get", "remote.origin.url"}, true, nil, baseEnv)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(remote) == "" {
		return "", ErrNoRemote
	}
	out, err := gitRunWithBase(cwd, []string{"ls-remote", "--", "origin", "refs/tags/" + tag, "refs/tags/" + tag + "^{}"}, true, nil, baseEnv)
	if err != nil {
		return "", err
	}
	var direct, peeled string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[1] {
		case "refs/tags/" + tag:
			direct = fields[0]
		case "refs/tags/" + tag + "^{}":
			peeled = fields[0]
		}
	}
	if peeled != "" {
		return peeled, nil
	}
	return direct, nil
}

func RemoteBranchShaWithEnv(cwd, branch string, baseEnv []string) (string, error) {
	if err := AssertValidGitRef("branch", branch); err != nil {
		return "", err
	}
	remote, err := gitRunWithBase(cwd, []string{"config", "--get", "remote.origin.url"}, true, nil, baseEnv)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(remote) == "" {
		return "", ErrNoRemote
	}
	output, err := gitRunWithBase(cwd, []string{"ls-remote", "--", "origin", "refs/heads/" + branch}, true, nil, baseEnv)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func RemoteBranchShaWithAuthEnv(cwd, branch string, baseEnv []string, auth types.GitAuth) (string, error) {
	if err := AssertValidGitRef("branch", branch); err != nil {
		return "", err
	}
	remote, err := gitRunWithBase(cwd, []string{"config", "--get", "remote.origin.url"}, true, auth, baseEnv)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(remote) == "" {
		return "", ErrNoRemote
	}
	output, err := gitRunWithBase(cwd, []string{"ls-remote", "--", "origin", "refs/heads/" + branch}, true, auth, baseEnv)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func RemoteTagShaWithAuthEnv(cwd, tag string, baseEnv []string, auth types.GitAuth) (string, error) {
	if err := AssertValidGitRef("tag", tag); err != nil {
		return "", err
	}
	remote, err := gitRunWithBase(cwd, []string{"config", "--get", "remote.origin.url"}, true, auth, baseEnv)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(remote) == "" {
		return "", ErrNoRemote
	}
	out, err := gitRunWithBase(cwd, []string{"ls-remote", "--", "origin", "refs/tags/" + tag, "refs/tags/" + tag + "^{}"}, true, auth, baseEnv)
	if err != nil {
		return "", err
	}
	var direct, peeled string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[1] {
		case "refs/tags/" + tag:
			direct = fields[0]
		case "refs/tags/" + tag + "^{}":
			peeled = fields[0]
		}
	}
	if peeled != "" {
		return peeled, nil
	}
	return direct, nil
}

// FileAtRef returns a repository file from a whitelisted revision.
func FileAtRef(cwd, ref, path string) ([]byte, error) {
	return FileAtRefWithEnv(cwd, ref, path, nil)
}

func FileAtRefWithEnv(cwd, ref, path string, baseEnv []string) ([]byte, error) {
	if _, err := RefShaWithEnv(cwd, ref, baseEnv); err != nil {
		return nil, err
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(path) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, hverrors.New("Invalid Git path: %s", jsQuote(path))
	}
	res := runCommand(cwd, childEnvWithBase(baseEnv, nil), "show", ref+":"+filepath.ToSlash(clean))
	if res.code != 0 {
		detail := res.stderr
		if detail == "" {
			detail = res.stdout
		}
		return nil, hverrors.New("git show %s failed:\n%s", ref+":"+filepath.ToSlash(clean), detail)
	}
	return []byte(res.stdout), nil
}

func CurrentBranchWithEnv(cwd string, baseEnv []string) (string, error) {
	branch, err := gitRunWithBase(cwd, []string{"branch", "--show-current"}, false, nil, baseEnv)
	if err != nil {
		return "", err
	}
	branch = strings.TrimSpace(branch)
	if branch != "" {
		return branch, nil
	}
	if v := os.Getenv("GITHUB_HEAD_REF"); v != "" {
		return v, nil
	}
	if os.Getenv("GITHUB_REF_TYPE") != "tag" {
		if v := os.Getenv("GITHUB_REF_NAME"); v != "" {
			return v, nil
		}
	}
	out, err := gitRunWithBase(cwd, []string{"rev-parse", "--abbrev-ref", "HEAD"}, false, nil, baseEnv)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func EnsureCleanWorkingTreeWithEnv(cwd string, managed map[string]bool, baseEnv []string) error {
	exempt := make(map[string]bool, len(managed))
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(cwd, p)
	}
	for p := range managed {
		exempt[abs(p)] = true
	}
	statusOut, err := gitRunWithBase(cwd, []string{"status", "--porcelain", "--untracked-files=all"}, false, nil, baseEnv)
	if err != nil {
		return err
	}
	var unexpected []string
	for _, line := range strings.Split(statusOut, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entry := line
		if len(line) > 3 {
			entry = strings.TrimSpace(line[3:])
		}
		if !exempt[abs(entry)] {
			unexpected = append(unexpected, line)
		}
	}
	for dir, scoped := range managed {
		if !scoped {
			continue
		}
		rel := dir
		if filepath.IsAbs(dir) {
			rel, err = filepath.Rel(cwd, dir)
			if err != nil {
				continue
			}
		}
		if rel == "." || rel == ".." || rel == "" || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		ignoredOut, err := gitRunWithBase(cwd, []string{
			"ls-files", "--others", "--ignored", "--exclude-standard", "--", rel,
		}, true, nil, baseEnv)
		if err != nil {
			continue
		}
		for _, p := range strings.Split(ignoredOut, "\n") {
			p = strings.TrimSpace(p)
			if p == "" || exempt[abs(p)] {
				continue
			}
			unexpected = append(unexpected, "?? "+p)
		}
	}
	if len(unexpected) > 0 {
		return hverrors.New("Working tree must be clean before release:\n%s", strings.Join(unexpected, "\n"))
	}
	return nil
}

func LatestTagWithEnv(cwd, pattern string, baseEnv []string) (string, error) {
	output, err := gitRunWithBase(cwd, []string{"describe", "--tags", "--abbrev=0", "--match", pattern}, true, nil, baseEnv)
	if err != nil {
		return "", err
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return "", ErrNoTag
	}
	return output, nil
}

func TagExistsWithEnv(cwd, tag string, baseEnv []string) (bool, error) {
	if err := AssertValidGitRef("tag", tag); err != nil {
		return false, err
	}
	return runCommand(cwd, childEnvWithBase(baseEnv, nil), "rev-parse", "--verify", "--quiet", "refs/tags/"+tag).code == 0, nil
}

func CommitMessageWithEnv(cwd, ref string, baseEnv []string) (string, error) {
	return gitRunWithBase(cwd, []string{"show", "-s", "--format=%B", ref}, false, nil, baseEnv)
}

func CreateReleaseCommitWithEnv(cwd, message string, baseEnv []string) error {
	if _, err := gitRunWithBase(cwd, []string{"add", "--all"}, false, nil, baseEnv); err != nil {
		return err
	}
	status, err := gitRunWithBase(cwd, []string{"status", "--porcelain"}, false, nil, baseEnv)
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	_, err = gitRunWithBase(cwd, []string{"commit", "-m", message}, false, nil, baseEnv)
	return err
}

func CreateAnnotatedTagWithEnv(cwd, tag, message string, baseEnv []string) error {
	if err := AssertValidGitRef("tag", tag); err != nil {
		return err
	}
	_, err := gitRunWithBase(cwd, []string{"tag", "-a", tag, "-m", message}, false, nil, baseEnv)
	return err
}

func PushReleaseWithEnv(cwd, branch string, tags []string, auth types.GitAuth, baseEnv []string) error {
	if err := AssertValidGitRef("branch", branch); err != nil {
		return err
	}
	for _, tag := range tags {
		if err := AssertValidGitRef("tag", tag); err != nil {
			return err
		}
	}
	args := []string{"push", "--atomic", "--no-verify", "--", "origin", "HEAD:refs/heads/" + branch}
	for _, tag := range tags {
		args = append(args, "refs/tags/"+tag)
	}
	_, err := gitRunWithBase(cwd, args, false, auth, baseEnv)
	return err
}

func OriginRepositoryWithEnv(cwd string, baseEnv []string) (string, error) {
	remote, err := gitRunWithBase(cwd, []string{"config", "--get", "remote.origin.url"}, true, nil, baseEnv)
	if err != nil {
		return "", err
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", nil
	}
	if m := sshRepoRE.FindStringSubmatch(remote); m != nil {
		return m[1], nil
	}
	if m := httpsRepoRE.FindStringSubmatch(remote); m != nil {
		return m[1], nil
	}
	return "", nil
}

func CommitsWithEnv(cwd, from, to string, baseEnv []string) ([]types.RawCommit, error) {
	rangeArg := to
	if from != "" {
		rangeArg = from + ".." + to
	}
	revList, err := gitRunWithBase(cwd, []string{"rev-list", "--reverse", rangeArg}, true, nil, baseEnv)
	if err != nil {
		return nil, err
	}
	if revList == "" {
		return []types.RawCommit{}, nil
	}
	commits := make([]types.RawCommit, 0)
	for _, hash := range strings.Split(revList, "\n") {
		subject, err := gitRunWithBase(cwd, []string{"show", "-s", "--format=%s", hash}, false, nil, baseEnv)
		if err != nil {
			return nil, err
		}
		body, err := gitRunWithBase(cwd, []string{"show", "-s", "--format=%b", hash}, false, nil, baseEnv)
		if err != nil {
			return nil, err
		}
		filesOut, err := gitRunWithBase(cwd, []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", hash}, true, nil, baseEnv)
		if err != nil {
			return nil, err
		}
		var files []string
		for _, f := range strings.Split(filesOut, "\n") {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
		commits = append(commits, types.RawCommit{Hash: hash, Subject: subject, Body: body, Files: files})
	}
	return commits, nil
}
