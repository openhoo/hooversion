package safefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteReplacesHardlinkWithoutChangingSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "manifest")
	if err := os.WriteFile(source, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, target); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := WriteFileAtomic(target, []byte("updated"), 0644); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" || string(updated) != "updated" {
		t.Fatalf("original=%q updated=%q", original, updated)
	}
	if os.PathSeparator != '\\' {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("permissions=%o", info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("temporary file leaked: %v", entries)
	}
}

func TestRegularFileReadRejectsSymlinkAndOversizedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("oversized"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(target, 2); err == nil {
		t.Fatal("oversized file accepted")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ReadRegularFile(link, 100); err == nil {
		t.Fatal("final symlink followed")
	}
	if err := WriteFileAtomic(link, []byte("changed"), 0600); err == nil {
		t.Fatal("final symlink overwritten")
	}
}

func TestContainedPathRejectsSymlinkParentsAndTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "linked")
	if err := RequireContainedPath(dir, "../outside"); err == nil {
		t.Fatal("traversal accepted")
	}
	if err := RequireContainedPath(dir, "missing/generated/file"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := RequireContainedPath(dir, "linked/new-file"); err == nil {
		t.Fatal("symlink parent accepted")
	}
}
