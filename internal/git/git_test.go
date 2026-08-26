package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	hverrors "github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/types"
)

// --- fixtures ---------------------------------------------------------------

// runRepoCmd runs a real git command in dir with an isolated config, failing
// the test on error.
func runRepoCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=", // isolate from developer/global git config
		"GIT_CONFIG_SYSTEM=",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bareRemote creates an empty bare repository usable as a push/ls-remote target.
func bareRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runRepoCmd(t, dir, "init", "-q", "--bare", ".")
	return dir
}

// commitAll stages and commits the current tree without touching files.
func (f *repoFixture) commitAll(t *testing.T, message string) {
	t.Helper()
	runRepoCmd(t, f.dir, "add", "--all")
	runRepoCmd(t, f.dir, "commit", "-q", "-m", message)
}

type repoFixture struct {
	dir     string
	commits []string // full SHAs, oldest first
}

// initRepo creates a real repository on main with n commits; commit i touches
// file<i>.txt (commit 0 also adds base.txt).
func initRepo(t *testing.T, n int) *repoFixture {
	t.Helper()
	dir := t.TempDir()
	runRepoCmd(t, dir, "init", "-q", "-b", "main")
	runRepoCmd(t, dir, "config", "user.email", "test@example.com")
	runRepoCmd(t, dir, "config", "user.name", "Test User")
	f := &repoFixture{dir: dir}
	for i := 0; i < n; i++ {
		files := []string{fmt.Sprintf("file%d.txt", i)}
		if i == 0 {
			files = append(files, "base.txt")
		}
		msg := fmt.Sprintf("feat: commit %d", i)
		if i == 1 {
			msg += "\n\nbody line one\nbody line two"
		}
		for _, name := range files {
			writeFile(t, filepath.Join(dir, name), fmt.Sprintf("content %d\n", i))
		}
		runRepoCmd(t, dir, "add", "--all")
		runRepoCmd(t, dir, "commit", "-q", "-m", msg)
		out := runRepoCmd(t, dir, "rev-parse", "HEAD")
		f.commits = append(f.commits, strings.TrimSpace(out))
	}
	return f
}

func (f *repoFixture) addCommit(t *testing.T, subjectAndBody string, files ...string) string {
	t.Helper()
	for _, name := range files {
		writeFile(t, filepath.Join(f.dir, name), "data "+name+"\n")
	}
	runRepoCmd(t, f.dir, "add", "--all")
	runRepoCmd(t, f.dir, "commit", "-q", "-m", subjectAndBody)
	out := strings.TrimSpace(runRepoCmd(t, f.dir, "rev-parse", "HEAD"))
	f.commits = append(f.commits, out)
	return out
}

func (f *repoFixture) tagAnnotated(t *testing.T, tag, message string) {
	t.Helper()
	runRepoCmd(t, f.dir, "tag", "-a", tag, "-m", message)
}

// --- AssertValidGitRef -------------------------------------------------------

func TestAssertValidGitRef(t *testing.T) {
	tests := []struct {
		name string
		kind string
		ref  string
		want bool // true = valid
	}{
		{"empty branch", "branch", "", false},
		{"empty tag", "tag", "", false},
		{"bare at", "branch", "@", false},
		{"leading dash", "branch", "-force", false},
		{"refs prefix", "branch", "refs/heads/x", false},
		{"refs prefix tag", "tag", "refs/tags/v1", false},
		{"leading slash", "branch", "/x", false},
		{"trailing slash", "branch", "x/", false},
		{"double slash", "branch", "a//b", false},
		{"dotdot", "tag", "v1..2", false},
		{"at brace", "branch", "a@{b", false},
		{"space", "branch", "a b", false},
		{"tab", "branch", "a\tb", false},
		{"tilde", "tag", "v1~", false},
		{"caret", "tag", "v^1", false},
		{"colon", "branch", "a:b", false},
		{"question", "branch", "a?b", false},
		{"star", "tag", "v*", false},
		{"backslash", "branch", `a\b`, false},
		{"bracket", "branch", "a[b", false},
		{"del char", "tag", "a\x7fb", false},
		{"control char", "branch", "a\x01b", false},
		{"single dot component", "branch", ".", false},
		{"double dot component", "branch", "..", false},
		{"inner dot component", "branch", "a/./b", false},
		{"inner dotdot component", "branch", "a/../b", false},
		{"leading dot component", "branch", ".hidden", false},
		{"trailing dot component", "tag", "v1.", false},
		{"lock suffix lower", "branch", "x.lock", false},
		{"lock suffix mixed case", "branch", "x.LOCK", false},
		{"lock suffix nested", "tag", "a/b.lock", false},
		{"plain branch", "branch", "main", true},
		{"feature slash", "branch", "feature/foo", true},
		{"deep nesting", "branch", "a/b/c", true},
		{"version tag", "tag", "v1.0.0", true},
		{"dashes and digits", "tag", "release-1.2.3", true},
		{"underscore", "branch", "_work", true},
		{"at not brace", "branch", "user@host", true},
		{"dots inside", "tag", "pkg.name@v1.2.3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AssertValidGitRef(tt.kind, tt.ref)
			if tt.want && err != nil {
				t.Fatalf("AssertValidGitRef(%q, %q) = %v, want valid", tt.kind, tt.ref, err)
			}
			if !tt.want && err == nil {
				t.Fatalf("AssertValidGitRef(%q, %q) = nil, want invalid", tt.kind, tt.ref)
			}
			if !tt.want {
				var exitErr *hverrors.ExitError
				if !errors.As(err, &exitErr) || exitErr.Code != 1 {
					t.Fatalf("invalid ref must yield ExitError code 1, got %T %v", err, err)
				}
				wantMsg := `Invalid Git ` + tt.kind + ` name: ` + jsQuote(tt.ref)
				if err.Error() != wantMsg {
					t.Fatalf("error message = %q, want %q", err.Error(), wantMsg)
				}
			}
		})
	}
}

// <, >, & are legal ref characters, so pair them with an actually rejected
// sequence (`..`) and assert JSON.stringify-style quoting stays unescaped.
func TestAssertValidGitRefErrorMessageNoHTMLEscaping(t *testing.T) {
	err := AssertValidGitRef("tag", "a<b>&c..d")
	if err == nil {
		t.Fatal("expected rejection for .. name")
	}
	got := err.Error()
	if !strings.Contains(got, `"a<b>&c..d"`) || strings.Contains(got, `\u003c`) {
		t.Fatalf("error message must keep < > & literal like JSON.stringify, got %q", got)
	}
}

// --- RefSha whitelist + peeling ----------------------------------------------

func TestRefShaWhitelist(t *testing.T) {
	f := initRepo(t, 3)
	head, headErr := HeadSha(f.dir)
	if headErr != nil {
		t.Fatal(headErr)
	}
	parent := f.commits[1]
	f.tagAnnotated(t, "v1.0.0", "release v1.0.0")

	tagObjectID := strings.TrimSpace(runRepoCmd(t, f.dir, "rev-parse", "refs/tags/v1.0.0"))
	if tagObjectID == head {
		t.Fatal("expected annotated tag object id to differ from commit id")
	}

	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr bool
	}{
		{"HEAD resolves", "HEAD", head, false},
		{"HEAD^ parent", "HEAD^", parent, false},
		{"full sha lowercase", strings.ToLower(head), head, false},
		{"full sha uppercase", strings.ToUpper(head), head, false},
		{"annotated tag peels to commit", "refs/tags/v1.0.0", head, false},
		{"branch head", "refs/heads/main", head, false},
		{"missing branch empty", "refs/heads/nope", "", false},
		{"missing tag empty", "refs/tags/v9.9.9", "", false},
		{"short name rejected", "main", "", true},
		{"HEAD~1 rejected", "HEAD~1", "", true},
		{"double caret rejected", "HEAD^^", "", true},
		{"short sha rejected", head[:8], "", true},
		{"invalid tag component", "refs/tags/bad..tag", "", true},
		{"invalid branch name", "refs/heads/a b", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RefSha(f.dir, tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RefSha(%q) = (%q, nil), want error", tt.ref, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RefSha(%q) errored: %v", tt.ref, err)
			}
			if got != tt.want {
				t.Fatalf("RefSha(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}

	err := func() error { _, e := RefSha(f.dir, "main"); return e }()
	var exitErr *hverrors.ExitError
	if !errors.As(err, &exitErr) || !strings.HasPrefix(err.Error(), "Invalid Git revision:") {
		t.Fatalf("short-name rejection must be ExitError with exact prefix, got %v", err)
	}
}

// --- LatestTag / TagExists ----------------------------------------------------

func TestLatestTagMatchPattern(t *testing.T) {
	f := initRepo(t, 4)
	if _, err := LatestTag(f.dir, "[0-9]*"); !errors.Is(err, ErrNoTag) {
		t.Fatalf("tagless repo must return ErrNoTag, got %v", err)
	}
	f.tagAnnotated(t, "other-1.0.0", "x")
	f.addCommit(t, "feat: two\n\nbody", "file2.txt")
	f.addCommit(t, "feat: three", "file3.txt")
	f.tagAnnotated(t, "v2.0.0", "two")

	got, err := LatestTag(f.dir, "v[0-9]*")
	if err != nil {
		t.Fatalf("LatestTag v pattern: %v", err)
	}
	if got != "v2.0.0" {
		t.Fatalf("LatestTag(v[0-9]*) = %q, want v2.0.0", got)
	}
	all, err := LatestTag(f.dir, "*[0-9]*") // matches other-1.0.0 AND v2.0.0
	if err != nil {
		t.Fatalf("LatestTag all pattern: %v", err)
	}
	if all != "v2.0.0" {
		t.Fatalf("LatestTag(*[0-9]*) = %q, want v2.0.0 (most recent overall)", all)
	}
	if _, err := LatestTag(f.dir, "nomatch-[0-9]*"); !errors.Is(err, ErrNoTag) {
		t.Fatalf("non-matching pattern must return ErrNoTag, got %v", err)
	}
}
func TestTagExists(t *testing.T) {
	f := initRepo(t, 1)
	f.tagAnnotated(t, "v1.0.0", "msg")
	ok, err := TagExists(f.dir, "v1.0.0")
	if err != nil || !ok {
		t.Fatalf("TagExists(v1.0.0) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = TagExists(f.dir, "v2.0.0")
	if err != nil || ok {
		t.Fatalf("TagExists(v2.0.0) = (%v, %v), want (false, nil)", ok, err)
	}
	if _, err := TagExists(f.dir, "bad..tag"); err == nil {
		t.Fatal("TagExists must reject invalid tag names")
	}
}

// --- OriginRepository ---------------------------------------------------------

func TestOriginRepositoryParsing(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"git@github.com:openhoo/hooversion.git", "openhoo/hooversion"},
		{"git@github.com:openhoo/hooversion", "openhoo/hooversion"},
		{"git@gitlab.com:a/b.git", "a/b"},
		{"https://github.com/openhoo/hooversion.git", "openhoo/hooversion"},
		{"http://github.com/openhoo/hooversion", "openhoo/hooversion"},
		{"https://git.corp.example/team/proj.git", "team/proj"},
		{"ssh://git@github.com/openhoo/hooversion.git", ""}, // ssh:// form unsupported, mirrors TS regexes
		{"git@host:onlyone", ""},                            // needs owner/repo shape
		{"https://host/single", ""},                         // needs owner/repo shape
		{"git@github.com:owner/.git", ""},                   // repo part must not start with dot
		{"git@github.com:owner/repo.extra.git", ""},         // dot before optional .git rejected
	}
	for _, tt := range tests {
		f := initRepo(t, 1)
		runRepoCmd(t, f.dir, "remote", "add", "origin", tt.url)
		got, err := OriginRepository(f.dir)
		if err != nil {
			t.Fatalf("OriginRepository(%q) errored: %v", tt.url, err)
		}
		if got != tt.want {
			t.Fatalf("OriginRepository(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}

	f := initRepo(t, 1)
	got, err := OriginRepository(f.dir)
	if err != nil || got != "" {
		t.Fatalf("no origin: got (%q, %v), want (\"\", nil)", got, err)
	}
}

// --- CurrentBranch -------------------------------------------------------------

func TestCurrentBranchFallbacks(t *testing.T) {
	f := initRepo(t, 1)

	branch, err := CurrentBranch(f.dir)
	if err != nil || branch != "main" {
		t.Fatalf("CurrentBranch = (%q, %v), want (main, nil)", branch, err)
	}

	// Detached HEAD: --show-current is empty on every supported git version,
	// so the env fallback chain is observable deterministically.
	detached := initRepo(t, 2)
	runRepoCmd(t, detached.dir, "checkout", "-q", "--detach", "HEAD")

	t.Setenv("GITHUB_HEAD_REF", "feature/pr-1")
	b, err := CurrentBranch(detached.dir)
	if err != nil || b != "feature/pr-1" {
		t.Fatalf("GITHUB_HEAD_REF fallback = (%q, %v), want feature/pr-1", b, err)
	}

	os.Unsetenv("GITHUB_HEAD_REF")
	t.Setenv("GITHUB_REF_TYPE", "branch")
	t.Setenv("GITHUB_REF_NAME", "beta")
	b, err = CurrentBranch(detached.dir)
	if err != nil || b != "beta" {
		t.Fatalf("GITHUB_REF_NAME fallback = (%q, %v), want beta", b, err)
	}

	t.Setenv("GITHUB_REF_TYPE", "tag") // tag builds must NOT use REF_NAME
	b, err = CurrentBranch(detached.dir)
	if err != nil || b != "HEAD" {
		t.Fatalf("tag REF_TYPE must fall through to rev-parse, got (%q, %v)", b, err)
	}
	os.Unsetenv("GITHUB_REF_TYPE")
	os.Unsetenv("GITHUB_REF_NAME")

	b, err = CurrentBranch(detached.dir)
	if err != nil || b != "HEAD" {
		t.Fatalf("detached fallback = (%q, %v), want HEAD", b, err)
	}
}

// --- Commits -------------------------------------------------------------------

func TestCommitsCollection(t *testing.T) {
	f := initRepo(t, 3)

	all, err := Commits(f.dir, "", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("Commits whole history = %d commits, want 3", len(all))
	}
	for i, c := range all {
		if c.Hash != f.commits[i] {
			t.Fatalf("commit[%d].Hash = %s, want %s (reverse order expected)", i, c.Hash, f.commits[i])
		}
		if c.Subject != fmt.Sprintf("feat: commit %d", i) {
			t.Fatalf("commit[%d].Subject = %q", i, c.Subject)
		}
	}
	if got := all[0].Files; len(got) != 2 || got[0] != "base.txt" || got[1] != "file0.txt" {
		t.Fatalf("root commit files = %v, want [base.txt file0.txt] (diff-tree --root, sorted)", got)
	}
	wantBody := "body line one\nbody line two"
	if got := all[1].Body; got != wantBody {
		t.Fatalf("commit[1].Body = %q, want %q", got, wantBody)
	}
	if got := all[1].Files; len(got) != 1 || got[0] != "file1.txt" {
		t.Fatalf("commit[1].Files = %v, want [file1.txt]", got)
	}

	sinceFirst, err := Commits(f.dir, f.commits[0], "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(sinceFirst) != 2 || sinceFirst[0].Hash != f.commits[1] || sinceFirst[1].Hash != f.commits[2] {
		t.Fatalf("range from..HEAD picked wrong commits: %+v", sinceFirst)
	}

	// Unknown range: rev-list runs with allowFailure, so result is empty, no error.
	unknown, err := Commits(f.dir, "ffffffffffffffffffffffffffffffffffffffff", "HEAD")
	if err != nil || len(unknown) != 0 {
		t.Fatalf("bad range must yield empty slice without error, got (%v, %v)", unknown, err)
	}
}

// --- EnsureCleanWorkingTree ------------------------------------------------------

func TestEnsureCleanWorkingTree(t *testing.T) {
	t.Run("clean tree passes", func(t *testing.T) {
		f := initRepo(t, 1)
		if err := EnsureCleanWorkingTree(f.dir, map[string]bool{}); err != nil {
			t.Fatalf("clean tree rejected: %v", err)
		}
	})

	t.Run("untracked dirt blocks", func(t *testing.T) {
		f := initRepo(t, 1)
		writeFile(t, filepath.Join(f.dir, "dirt.txt"), "x\n")
		err := EnsureCleanWorkingTree(f.dir, map[string]bool{})
		if err == nil {
			t.Fatal("untracked file must block release")
		}
		if !strings.HasPrefix(err.Error(), "Working tree must be clean before release:\n") {
			t.Fatalf("error text = %q", err.Error())
		}
		if !strings.Contains(err.Error(), "?? dirt.txt") {
			t.Fatalf("error must list porcelain line, got %q", err.Error())
		}
		var exitErr *hverrors.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("clean-tree failure must be ExitError, got %T", err)
		}
	})

	t.Run("modified tracked file blocks", func(t *testing.T) {
		f := initRepo(t, 1)
		writeFile(t, filepath.Join(f.dir, "base.txt"), "changed\n")
		if err := EnsureCleanWorkingTree(f.dir, map[string]bool{}); err == nil {
			t.Fatal("modified tracked file must block release")
		}
	})

	t.Run("managed untracked paths exempt", func(t *testing.T) {
		f := initRepo(t, 1)
		writeFile(t, filepath.Join(f.dir, ".hooversion", "outputs.json"), "{}\n")
		writeFile(t, filepath.Join(f.dir, ".release-version"), "1.2.3\n")
		managed := map[string]bool{
			".hooversion/outputs.json": false,
			".release-version":         false,
		}
		if err := EnsureCleanWorkingTree(f.dir, managed); err != nil {
			t.Fatalf("managed outputs must be exempt: %v", err)
		}
	})

	t.Run("gitignored file inside scoped outputDir blocks", func(t *testing.T) {
		f := initRepo(t, 1)
		runRepoCmd(t, f.dir, "add", "--all")
		writeFile(t, filepath.Join(f.dir, ".gitignore"), ".hooversion/\n")
		f.commitAll(t, "chore: ignore output dir")
		writeFile(t, filepath.Join(f.dir, ".hooversion", "stale.json"), "stale\n") // ignored

		// Without a scoped dir entry the ignored file hides silently.
		if err := EnsureCleanWorkingTree(f.dir, map[string]bool{}); err != nil {
			t.Fatalf("ignored file outside scoped rule must not block: %v", err)
		}
		// Scoped .hooversion directory surfaces it as unexpected.
		err := EnsureCleanWorkingTree(f.dir, map[string]bool{".hooversion": true})
		if err == nil {
			t.Fatal("gitignored stale payload inside outputDir must block release")
		}
		if !strings.Contains(err.Error(), "?? ") ||
			!strings.Contains(err.Error(), filepath.Join(".hooversion", "stale.json")) {
			t.Fatalf("error must list ignored payload as unexpected, got %q", err.Error())
		}
	})

	t.Run("ignored files elsewhere never block", func(t *testing.T) {
		f := initRepo(t, 1)
		writeFile(t, filepath.Join(f.dir, ".gitignore"), "build.log\n")
		f.commitAll(t, "chore: ignore build log")
		writeFile(t, filepath.Join(f.dir, "build.log"), "noise\n")
		if err := EnsureCleanWorkingTree(f.dir, map[string]bool{".hooversion": true}); err != nil {
			t.Fatalf("ignored file outside scoped dirs must not block: %v", err)
		}
	})
}

// --- PushRelease -----------------------------------------------------------------

// argRecorder installs a PATH shim that logs every git invocation (args
// NUL-separated, plus selected env vars) and then execs the REAL git binary,
// so argv order and env merging are observable without weakening behavior.
type argRecorder struct {
	argLog string // NUL-separated argv per invocation, "--\n" between calls
	envLog string
}

func newArgRecorder(t *testing.T) *argRecorder {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("real git binary required: %v", err)
	}
	shimDir := t.TempDir()
	rec := &argRecorder{
		argLog: filepath.Join(shimDir, "args.log"),
		envLog: filepath.Join(shimDir, "env.log"),
	}
	script := "#!/bin/sh\n" +
		`for a in "$@"; do printf '%s\0' "$a" >> "$HV_ARG_LOG"; done` + "\n" +
		`printf '\n' >> "$HV_ARG_LOG"` + "\n" +
		`printf '%s' "${VERSIONHOO_GIT_TOKEN-}" >> "$HV_ENV_LOG"` + "\n" +
		"exec " + realGit + ` "$@"` + "\n"
	shim := filepath.Join(shimDir, "git")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HV_ARG_LOG", rec.argLog)
	t.Setenv("HV_ENV_LOG", rec.envLog)
	return rec
}

func (r *argRecorder) invocations(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(r.argLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		calls = append(calls, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
	}
	return calls
}

func TestPushReleaseAtomicArgvOrder(t *testing.T) {
	f := initRepo(t, 2)
	f.tagAnnotated(t, "v1.0.0", "one")
	f.tagAnnotated(t, "v2.0.0", "two")
	remote := bareRemote(t)
	runRepoCmd(t, f.dir, "remote", "add", "origin", remote)

	rec := newArgRecorder(t)
	auth := types.GitAuth{"VERSIONHOO_GIT_TOKEN": "tok123"}
	if err := PushRelease(f.dir, "main", []string{"v1.0.0", "v2.0.0"}, auth); err != nil {
		t.Fatalf("PushRelease failed: %v", err)
	}

	calls := rec.invocations(t)
	if len(calls) != 1 {
		t.Fatalf("PushRelease must issue exactly ONE git call, got %d: %v", len(calls), calls)
	}
	want := []string{"push", "--atomic", "--no-verify", "--", "origin",
		"HEAD:refs/heads/main", "refs/tags/v1.0.0", "refs/tags/v2.0.0"}
	if strings.Join(calls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("push argv = %v, want %v", calls[0], want)
	}

	// Auth env var reached the child process (spawnSync env merge semantics).
	envData, err := os.ReadFile(rec.envLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(envData) != "tok123" {
		t.Fatalf("VERSIONHOO_GIT_TOKEN not merged into child env, got %q", envData)
	}

	// Behavioral proof: remote carries branch + both peeled tags.
	out := runRepoCmd(t, remote, "rev-parse", "--verify", "-q", "refs/heads/main^{commit}")
	if got := strings.TrimSpace(out); got != f.commits[len(f.commits)-1] {
		t.Fatalf("remote main = %s, want local HEAD %s", got, f.commits[len(f.commits)-1])
	}
	for _, tag := range []string{"v1.0.0", "v2.0.0"} {
		runRepoCmd(t, remote, "rev-parse", "--verify", "-q", "refs/tags/"+tag+"^{commit}")
	}
}

func TestPushReleaseValidationBeforeNetwork(t *testing.T) {
	f := initRepo(t, 1)
	remote := bareRemote(t)
	runRepoCmd(t, f.dir, "remote", "add", "origin", remote)

	rec := newArgRecorder(t)
	cases := []struct {
		name   string
		branch string
		tags   []string
	}{
		{"invalid branch", "bad..branch", nil},
		{"invalid tag", "main", []string{"ok", "bad tag"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := PushRelease(f.dir, tc.branch, tc.tags, nil)
			if err == nil {
				t.Fatal("invalid names must be rejected")
			}
			if calls := rec.invocations(t); len(calls) != 0 {
				t.Fatalf("validation must happen before any git call, saw %v", calls)
			}
		})
	}
}

func TestPushReleaseWithoutTagsStillAtomic(t *testing.T) {
	f := initRepo(t, 1)
	remote := bareRemote(t)
	runRepoCmd(t, f.dir, "remote", "add", "origin", remote)

	rec := newArgRecorder(t)
	if err := PushRelease(f.dir, "main", nil, nil); err != nil {
		t.Fatalf("PushRelease without tags failed: %v", err)
	}
	calls := rec.invocations(t)
	want := []string{"push", "--atomic", "--no-verify", "--", "origin", "HEAD:refs/heads/main"}
	if len(calls) != 1 || strings.Join(calls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", calls, want)
	}
	envData, _ := os.ReadFile(rec.envLog)
	if string(envData) != "" {
		t.Fatalf("nil auth must merge nothing extra, env log = %q", envData)
	}
	runRepoCmd(t, remote, "rev-parse", "--verify", "-q", "refs/heads/main^{commit}")
}

// --- RemoteBranchSha -------------------------------------------------------------

func TestRemoteBranchShaTriState(t *testing.T) {
	f := initRepo(t, 2)
	head := f.commits[1]

	if _, err := RemoteBranchSha(f.dir, "main"); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("no origin must return ErrNoRemote, got %v", err)
	}

	remote := bareRemote(t)
	runRepoCmd(t, f.dir, "remote", "add", "origin", remote)

	sha, err := RemoteBranchSha(f.dir, "main")
	if err != nil || sha != "" {
		t.Fatalf("unpushed branch must give (\"\", nil), got (%q, %v)", sha, err)
	}

	if err := PushRelease(f.dir, "main", nil, nil); err != nil {
		t.Fatalf("PushRelease(main): %v", err)
	}
	sha, err = RemoteBranchSha(f.dir, "main")
	if err != nil || sha != head {
		t.Fatalf("RemoteBranchSha = (%q, %v), want (%q, nil)", sha, err, head)
	}
	sha, err = RemoteBranchSha(f.dir, "ghost")
	if err != nil || sha != "" {
		t.Fatalf("missing remote branch = (%q, %v), want (\"\", nil)", sha, err)
	}
	if _, err := RemoteBranchSha(f.dir, "bad..name"); err == nil || errors.Is(err, ErrNoRemote) {
		t.Fatalf("invalid branch must fail validation, got %v", err)
	}
}

// --- CreateAnnotatedTag / CommitMessage / misc helpers ----------------------------

func TestCreateAnnotatedTag(t *testing.T) {
	f := initRepo(t, 1)
	if err := CreateAnnotatedTag(f.dir, "v1.0.0", "hooversion v1.0.0"); err != nil {
		t.Fatalf("CreateAnnotatedTag: %v", err)
	}
	objType := strings.TrimSpace(runRepoCmd(t, f.dir, "cat-file", "-t", "refs/tags/v1.0.0"))
	if objType != "tag" {
		t.Fatalf("tag object type = %q, want tag (annotated)", objType)
	}
	content := runRepoCmd(t, f.dir, "tag", "-l", "--format=%(contents)", "v1.0.0")
	if !strings.HasPrefix(content, "hooversion v1.0.0") {
		t.Fatalf("tag message = %q", content)
	}
	if err := CreateAnnotatedTag(f.dir, "v2..0", "x"); err == nil {
		t.Fatal("invalid tag name must be rejected before running git")
	}
}

func TestCommitMessageAndLastCommit(t *testing.T) {
	f := initRepo(t, 2)
	msg, err := CommitMessage(f.dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if msg != "feat: commit 1\n\nbody line one\nbody line two" {
		t.Fatalf("CommitMessage(HEAD) = %q", msg)
	}
	last, err := LastCommit(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if last.Hash != f.commits[1] || last.Subject != "feat: commit 1" || len(last.Files) != 1 {
		t.Fatalf("LastCommit = %+v", last)
	}
}

func TestIsGitRepositoryAndFailureMessage(t *testing.T) {
	f := initRepo(t, 1)
	if !IsGitRepository(f.dir) {
		t.Fatal("fixture must be recognized as a work tree")
	}
	notARepo := t.TempDir()
	if IsGitRepository(notARepo) {
		t.Fatal("temp dir must not be a work tree")
	}
	_, err := HeadSha(notARepo)
	if err == nil {
		t.Fatal("HeadSha outside a repo must fail")
	}
	wantPrefix := "git rev-parse HEAD failed:\n"
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Fatalf("failure text = %q, want prefix %q", err.Error(), wantPrefix)
	}
	var exitErr *hverrors.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("git failures must surface as ExitError, got %T", err)
	}
}

func TestCreateReleaseCommit(t *testing.T) {
	f := initRepo(t, 1)
	before := f.commits[0]
	// No changes -> no commit.
	if err := CreateReleaseCommit(f.dir, "chore(release): noop"); err != nil {
		t.Fatal(err)
	}
	head, _ := HeadSha(f.dir)
	if head != before {
		t.Fatal("clean tree must not create a commit")
	}
	writeFile(t, filepath.Join(f.dir, "base.txt"), "released\n")
	if err := CreateReleaseCommit(f.dir, "chore(release): pkg 1.2.3"); err != nil {
		t.Fatal(err)
	}
	msg, _ := CommitMessage(f.dir, "HEAD")
	if msg != "chore(release): pkg 1.2.3" {
		t.Fatalf("release commit message = %q", msg)
	}
}
