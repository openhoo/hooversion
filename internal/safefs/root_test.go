package safefs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPinnedRootSurvivesCheckoutRename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps open checkout directories from renaming")
	}
	base := t.TempDir()
	checkout := filepath.Join(base, "checkout")
	outside := filepath.Join(base, "outside")
	os.Mkdir(checkout, 0755)
	os.Mkdir(outside, 0755)
	root, err := OpenRoot(checkout)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	moved := filepath.Join(base, "moved")
	if err := os.Rename(checkout, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, checkout); err != nil {
		t.Fatal(err)
	}
	if err := root.CheckIdentity(); err == nil {
		t.Fatal("checkout replacement accepted")
	}
	if err := root.WriteFileAtomic("version", []byte("1.2.3"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "version")); !os.IsNotExist(err) {
		t.Fatal("escaped pinned root")
	}
	if data, err := os.ReadFile(filepath.Join(moved, "version")); err != nil || string(data) != "1.2.3" {
		t.Fatalf("pinned write: %q %v", data, err)
	}
}
func TestRootRejectsSwappedParentAndFinalLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges")
	}
	checkout, outside := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(checkout, "pkg"), 0755)
	external := filepath.Join(outside, "version")
	os.WriteFile(external, []byte("keep"), 0600)
	root, err := OpenRoot(checkout)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	os.Rename(filepath.Join(checkout, "pkg"), filepath.Join(checkout, "original"))
	os.Symlink(outside, filepath.Join(checkout, "pkg"))
	if err := root.WriteFileAtomic("pkg/version", []byte("bad"), 0600); err == nil {
		t.Fatal("parent link accepted")
	}
	os.Symlink(external, filepath.Join(checkout, "version"))
	if _, err := root.ReadRegularFile("version", 100); err == nil {
		t.Fatal("final link read accepted")
	}
	if err := root.WriteFileAtomic("version", []byte("bad"), 0600); err == nil {
		t.Fatal("final link write accepted")
	}
	data, _ := os.ReadFile(external)
	if string(data) != "keep" {
		t.Fatal("external content changed")
	}
}
func TestRootAtomicReplacementAndLimits(t *testing.T) {
	checkout := t.TempDir()
	root, err := OpenRoot(checkout)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	os.WriteFile(filepath.Join(checkout, "version"), []byte("old"), 0600)
	linked := filepath.Join(checkout, "other")
	if err := os.Link(filepath.Join(checkout, "version"), linked); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFileAtomic("version", []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(linked)
	if string(data) != "old" {
		t.Fatal("modified hardlink target")
	}
	if _, err := root.ReadRegularFile("version", 2); err == nil {
		t.Fatal("oversized read accepted")
	}
	if err := root.WriteFileAtomic("../outside", []byte("x"), 0600); err == nil {
		t.Fatal("escape accepted")
	}
	if err := root.RestoreFile("version", []byte("restored"), 0644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(checkout, "version"))
		if info.Mode().Perm() != 0644 {
			t.Fatal("original permissions not restored")
		}
	}
}
func TestLockExcludesOwnersAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.lock")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := AcquireLock(path); err == nil {
		second.Close()
		first.Close()
		t.Fatal("second owner acquired lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("persistent lock missing")
	}
}
