package git

import (
	"fmt"
	"github.com/openhoo/hooversion/internal/types"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBatchHistoryMatchesSingleCommitCollection(t *testing.T) {
	f := initRepo(t, 3)
	// A filename matching an object identity must remain an opaque filename.
	f.addCommit(t, "fix: hash filename\n\nline one\nline two", f.commits[0])
	runRepoCmd(t, f.dir, "commit", "--allow-empty", "-m", "chore: empty")
	runRepoCmd(t, f.dir, "checkout", "-b", "side")
	f.addCommit(t, "feat: side", "side.txt")
	runRepoCmd(t, f.dir, "checkout", "main")
	f.addCommit(t, "fix: main", "main.txt")
	runRepoCmd(t, f.dir, "merge", "--no-ff", "side", "-m", "merge side")
	collected, err := Commits(f.dir, "", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, commit := range collected {
		want, err := readCommit(f.dir, commit.Hash, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(commit, want) {
			t.Fatalf("batch mismatch\ngot: %#v\nwant: %#v", commit, want)
		}
	}
}

func TestBatchHistoryUsesBoundedSubprocesses(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX recorder")
	}
	f := initRepo(t, 20)
	rec := newArgRecorder(t)
	commits, err := Commits(f.dir, "", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	calls := rec.invocations(t)
	if len(commits) != 20 || len(calls) != 3 {
		t.Fatalf("commits=%d commands=%d", len(commits), len(calls))
	}
}

func TestBatchParsersRejectMalformedRecords(t *testing.T) {
	hash := strings.Repeat("a", 40)
	for _, input := range []string{hash, hash + "\x00subject\x00", hash + "\x00s\x00b\x00" + hash + "\x00s\x00b\x00", "invalid\x00s\x00b\x00"} {
		if _, err := parseCommitMetadata(input); err == nil {
			t.Errorf("accepted metadata %q", input)
		}
	}
	for _, input := range []string{hash, hash + "\x00:broken\x00file\x00", strings.Repeat("b", 40) + "\x00", hash + "\x00" + hash + "\x00"} {
		if err := parseCommitDiffs(input, []types.RawCommit{{Hash: hash}}); err == nil {
			t.Errorf("accepted diff %q", input)
		}
	}
}

func BenchmarkBatchHistory(b *testing.B) {
	dir := b.TempDir()
	run := func(args ...string) {
		b.Helper()
		res := runCommand(dir, append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.test"), args...)
		if res.code != 0 {
			b.Fatal(res.stderr)
		}
	}
	run("init", "-b", "main")
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte(fmt.Sprint(i)), 0600); err != nil {
			b.Fatal(err)
		}
		run("add", "file")
		run("commit", "-m", "fix: commit")
	}
	list, err := gitRun(dir, []string{"rev-list", "--reverse", "HEAD"}, false, nil)
	if err != nil {
		b.Fatal(err)
	}
	hashes := strings.Split(list, "\n")
	b.Run("batched", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := Commits(dir, "", "HEAD"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("per_commit", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, hash := range hashes {
				if _, err := readCommit(dir, hash, nil); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
