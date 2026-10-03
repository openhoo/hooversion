package release

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hooversion/internal/changelog"
	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/manifest"
	"github.com/openhoo/hooversion/internal/output"
	"github.com/openhoo/hooversion/internal/types"
)

func snapshotTransaction(t *testing.T, cwd string, cfg *types.NormalizedConfig, p *types.ReleasePlan) (*transaction, func()) {
	t.Helper()
	txn, owner, err := openTransaction(context.Background(), cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := manifest.ManagedPathsWithFS(cwd, cfg.Packages, txn.files)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range cfg.Packages {
		paths = append(paths, pkg.Changelog)
	}
	store := output.Store{Cwd: cwd, OutputDir: cfg.OutputDir, Root: txn.files}
	dest, err := store.Destinations(p.Releases)
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, dest...)
	if err := txn.snapshot(context.Background(), cfg, p, paths); err != nil {
		t.Fatal(err)
	}
	return txn, func() { txn.close(); owner.Close() }
}
func assertRecoveredTree(t *testing.T, cwd, source string) {
	t.Helper()
	if got := gitOut(t, cwd, "rev-parse", "HEAD"); got != source {
		t.Fatalf("HEAD changed: %s != %s", got, source)
	}
	if got := gitOut(t, cwd, "status", "--porcelain"); got != "" {
		t.Fatalf("recovery left working tree changes: %s", got)
	}
	if got := appManifestVersion(t, cwd); got != "1.0.0" {
		t.Fatalf("recovered version: %s", got)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".git", transactionName)); !os.IsNotExist(err) {
		t.Fatalf("journal remains: %v", err)
	}
}
func TestHookFailuresRollbackTrackedAndCreatedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix hook fixtures")
	}
	for _, phase := range []string{"beforeRelease", "afterVersion"} {
		t.Run(phase, func(t *testing.T) {
			cwd := seedAppRepo(t)
			cfg := singleAppConfig(false)
			p, err := CreatePlanForTest(cwd, cfg)
			if err != nil {
				t.Fatal(err)
			}
			command := "printf damaged > app.ts; printf new > hook-created.txt; exit 3"
			if phase == "beforeRelease" {
				cfg.Hooks.BeforeRelease = []string{command}
			} else {
				cfg.Hooks.AfterVersion = []string{command}
			}
			_, err = Execute(cwd, cfg, p, Options{NoPushSet: true, NoGitHubSet: true})
			if err == nil || !strings.Contains(err.Error(), "Hook failed") {
				t.Fatalf("missing hook failure: %v", err)
			}
			assertRecoveredTree(t, cwd, p.SourceSha)
			if _, err := os.Stat(filepath.Join(cwd, "hook-created.txt")); !os.IsNotExist(err) {
				t.Fatal("hook-created file survived rollback")
			}
		})
	}
}
func TestRecoveryRestoresManagedOutputPayload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix hook fixtures")
	}
	cwd := seedAppRepo(t)
	cfg := singleAppConfig(false)
	writeFile(t, filepath.Join(cwd, ".hooversion", "outputs.json"), `{"releases":[{"tag":"old","notesPath":".hooversion/old-notes.md"}]}`)
	writeFile(t, filepath.Join(cwd, ".hooversion", "old-notes.md"), "previous notes")
	writeFile(t, filepath.Join(cwd, ".release-version"), "previous version")
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Hooks.AfterVersion = []string{"exit 2"}
	_, err = Execute(cwd, cfg, p, Options{NoPushSet: true, NoGitHubSet: true})
	if err == nil {
		t.Fatal("failed hook accepted")
	}
	if got := readFileString(t, cwd, ".hooversion", "old-notes.md"); got != "previous notes" {
		t.Fatal("old payload lost")
	}
	if got := readFileString(t, cwd, ".release-version"); got != "previous version" {
		t.Fatal("old version output lost")
	}
}
func TestInterruptedCommitAndPartialTagsRecoverThenRelease(t *testing.T) {
	cwd := makeRepo(t)
	cfg := singleAppConfig(false)
	cfg.Packages = []types.NormalizedPackageConfig{nodePkg("one", "one", "one/package.json"), nodePkg("two", "two", "two/package.json")}
	for i := range cfg.Packages {
		cfg.Packages[i].Changelog = filepath.Join(cfg.Packages[i].Path, "CHANGELOG.md")
		writeFile(t, filepath.Join(cwd, cfg.Packages[i].Manifest), `{"name":"`+cfg.Packages[i].Name+`","version":"1.0.0"}`)
	}
	commitAll(t, cwd, "initial import")
	for _, pkg := range cfg.Packages {
		gitOut(t, cwd, "tag", "-a", pkg.Name+"@v1.0.0", "-m", "initial")
	}
	writeFile(t, filepath.Join(cwd, "one", "code"), "one")
	writeFile(t, filepath.Join(cwd, "two", "code"), "two")
	commitAll(t, cwd, "fix: repair both")
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Releases) != 2 {
		t.Fatal("expected two releases")
	}
	txn, closeTxn := snapshotTransaction(t, cwd, cfg, p)
	if err := txn.advance("committing"); err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Releases {
		pkg := r.Package
		pkg.Manifest = filepath.Join(cwd, pkg.Manifest)
		if err := manifest.UpdateVersionWithFS(pkg, r.NextVersion, txn.files); err != nil {
			t.Fatal(err)
		}
		if err := changelog.UpdateWithFS(filepath.Join(cwd, r.Package.Changelog), r.Notes, r.Package.Name, txn.files); err != nil {
			t.Fatal(err)
		}
	}
	if err := git.CreateReleaseCommit(cwd, CommitMessage(p)); err != nil {
		t.Fatal(err)
	}
	if err := git.CreateAnnotatedTag(cwd, p.Releases[0].Tag, "partial tag"); err != nil {
		t.Fatal(err)
	}
	// Simulate process death before journaling the new HEAD/tag set.
	closeTxn()
	result, err := Execute(cwd, cfg, p, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Published || len(result.Plan.Releases) != 2 {
		t.Fatal("interrupted release not recovered")
	}
	if got := gitOut(t, cwd, "rev-list", "--count", "HEAD"); got != "3" {
		t.Fatalf("extra release commits: %s", got)
	}
	for _, r := range p.Releases {
		if got := gitOut(t, cwd, "rev-parse", r.Tag+"^{commit}"); got != gitOut(t, cwd, "rev-parse", "HEAD") {
			t.Fatal("partial tag wasn't replaced by recovered release")
		}
	}
}
func TestRecoveryRefusesUnrelatedCommitAndKeepsJournal(t *testing.T) {
	cwd := seedAppRepo(t)
	cfg := singleAppConfig(false)
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	txn, closeTxn := snapshotTransaction(t, cwd, cfg, p)
	if err := txn.advance("mutating"); err != nil {
		t.Fatal(err)
	}
	closeTxn()
	gitOut(t, cwd, "commit", "--allow-empty", "-m", "chore: operator commit")
	changed := gitOut(t, cwd, "rev-parse", "HEAD")
	_, err = Execute(cwd, cfg, p, Options{NoPushSet: true, NoGitHubSet: true})
	if err == nil || !strings.Contains(err.Error(), "refuses changed HEAD") {
		t.Fatalf("unrelated HEAD overwritten: %v", err)
	}
	if got := gitOut(t, cwd, "rev-parse", "HEAD"); got != changed {
		t.Fatal("operator commit lost")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".git", transactionName)); err != nil {
		t.Fatal("recovery journal lost")
	}
}
func TestUncertainPushRetainsCommitAndResumesWithoutSecondCommit(t *testing.T) {
	cwd := seedAppRepo(t)
	remote := makeBareRemote(t)
	gitOut(t, cwd, "remote", "add", "origin", remote)
	gitOut(t, cwd, "push", "origin", "main", "--tags")
	writeFile(t, filepath.Join(remote, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	os.Chmod(filepath.Join(remote, "hooks", "pre-receive"), 0755)
	cfg := singleAppConfig(true)
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Execute(cwd, cfg, p, Options{NoGitHubSet: true})
	if err == nil {
		t.Fatal("remote rejection accepted")
	}
	releaseHead := gitOut(t, cwd, "rev-parse", "HEAD")
	if releaseHead == p.SourceSha {
		t.Fatal("uncertain push rolled back release commit")
	}
	raw, err := os.ReadFile(filepath.Join(cwd, ".git", transactionName))
	if err != nil {
		t.Fatal(err)
	}
	var state transactionState
	if err := json.Unmarshal(raw, &state); err != nil || state.Phase != "external" {
		t.Fatalf("journal phase: %s %v", state.Phase, err)
	}
	os.Remove(filepath.Join(remote, "hooks", "pre-receive"))
	rerun, err := Execute(cwd, cfg, p, Options{NoGitHubSet: true})
	if err != nil || !rerun.Published {
		t.Fatalf("retry: %v", err)
	}
	if got := gitOut(t, cwd, "rev-parse", "HEAD"); got != releaseHead {
		t.Fatal("retry added a commit")
	}
	if got := gitOut(t, remote, "rev-parse", "refs/heads/main"); got != releaseHead {
		t.Fatal("remote does not match release")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".git", transactionName)); !os.IsNotExist(err) {
		t.Fatal("successful retry retained journal")
	}
}
func TestCanceledHookCompletesRollbackBeforeReturning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix hook fixtures")
	}
	cwd := seedAppRepo(t)
	cfg := singleAppConfig(false)
	cfg.Hooks.AfterVersion = []string{"printf damaged > app.ts; sleep 30"}
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	_, err = Execute(cwd, cfg, p, Options{Context: ctx, NoPushSet: true, NoGitHubSet: true})
	if err == nil {
		t.Fatal("canceled hook succeeded")
	}
	assertRecoveredTree(t, cwd, p.SourceSha)
}
func TestLinkedWorktreesShareReleaseOwnership(t *testing.T) {
	cwd := seedAppRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitOut(t, cwd, "worktree", "add", "-b", "other", linked)
	txn, owner, err := openTransaction(context.Background(), cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	defer txn.close()
	if other, lock, err := openTransaction(context.Background(), linked, nil); err == nil {
		other.close()
		lock.Close()
		t.Fatal("linked worktree bypassed release lock")
	}
}

// The subprocess intentionally exits without defers, as a killed releaser
// would. The parent must reacquire its OS lock and recover persisted mutations.
func TestCrashTransactionHelper(t *testing.T) {
	cwd := os.Getenv("HOOVERSION_CRASH_TEST_REPO")
	if cwd == "" {
		return
	}
	cfg := singleAppConfig(false)
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	txn, _ := snapshotTransaction(t, cwd, cfg, p)
	if err := txn.advance("mutating"); err != nil {
		t.Fatal(err)
	}
	pkg := p.Releases[0].Package
	pkg.Manifest = filepath.Join(cwd, pkg.Manifest)
	if err := manifest.UpdateVersionWithFS(pkg, p.Releases[0].NextVersion, txn.files); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
func TestRealProcessCrashRecovery(t *testing.T) {
	cwd := seedAppRepo(t)
	cfg := singleAppConfig(false)
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "-test.run=^TestCrashTransactionHelper$")
	child.Env = append(os.Environ(), "HOOVERSION_CRASH_TEST_REPO="+cwd)
	if data, err := child.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v %s", err, data)
	}
	if got := appManifestVersion(t, cwd); got != "1.0.1" {
		t.Fatal("child did not leave interrupted mutation")
	}
	run, err := Execute(cwd, cfg, p, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil || !run.Published {
		t.Fatalf("crash recovery: %v", err)
	}
	if got := gitOut(t, cwd, "rev-list", "--count", "HEAD"); got != "3" {
		t.Fatal("crash recovery added an extra commit")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".git", transactionName)); !os.IsNotExist(err) {
		t.Fatal("completed recovery left journal")
	}
}
func TestPublishedJournalRefusesHeadReset(t *testing.T) {
	cwd := seedAppRepo(t)
	cfg := singleAppConfig(false)
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	txn, closeTxn := snapshotTransaction(t, cwd, cfg, p)
	gitOut(t, cwd, "commit", "--allow-empty", "-m", CommitMessage(p))
	if err := txn.releaseHead(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := txn.advance("external"); err != nil {
		t.Fatal(err)
	}
	closeTxn()
	gitOut(t, cwd, "reset", "--hard", p.SourceSha)
	_, err = Execute(cwd, cfg, p, Options{NoPushSet: true, NoGitHubSet: true})
	if err == nil || !strings.Contains(err.Error(), "requires the recorded release commit") {
		t.Fatalf("publication source reset accepted: %v", err)
	}
}

func TestRollbackRestoresOriginalStagedManagedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix hook fixtures")
	}
	cwd := seedAppRepo(t)
	writeFile(t, filepath.Join(cwd, ".release-version"), "baseline\n")
	commitAll(t, cwd, "chore: track managed version output")
	writeFile(t, filepath.Join(cwd, ".release-version"), "staged\n")
	gitOut(t, cwd, "add", ".release-version")
	cfg := singleAppConfig(false)
	cfg.Hooks.AfterVersion = []string{"exit 3"}
	p, err := CreatePlanForTest(cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(cwd, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Execute(cwd, cfg, p, Options{NoPushSet: true, NoGitHubSet: true})
	if err == nil {
		t.Fatal("failed hook accepted")
	}
	after, err := os.ReadFile(filepath.Join(cwd, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("Git staging state changed during rollback")
	}
	if got := gitOut(t, cwd, "show", ":.release-version"); got != "staged" {
		t.Fatal("original staging lost")
	}
}
