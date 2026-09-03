//go:build unix

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWebhookSpoolRejectsForeignOwnedRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a foreign-owned test directory")
	}
	parent := privateSpoolTestDir(t)
	owned := filepath.Join(parent, "owned")
	if err := os.Mkdir(owned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(owned, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWebhookSpool(owned, 1024); err == nil {
		t.Fatal("foreign-owned spool root accepted")
	}
}

func TestWebhookSpoolCreatesMissingComponentsSafely(t *testing.T) {
	parent := privateSpoolTestDir(t)
	path := filepath.Join(parent, "one", "two", "spool")
	if err := prepareWebhookSpoolDir(path); err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{
		filepath.Join(parent, "one"),
		filepath.Join(parent, "one", "two"),
		path,
	} {
		info, err := os.Lstat(component)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("created unsafe spool component %s", component)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("created spool component %s with mode %o", component, info.Mode().Perm())
		}
	}
}

func TestWebhookSpoolRejectsAttackerOwnedIntermediate(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a foreign-owned intermediate")
	}
	parent := privateSpoolTestDir(t)
	attacker := filepath.Join(parent, "attacker")
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(attacker, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(attacker, "nested", "spool")
	if err := prepareWebhookSpoolDir(path); err == nil {
		t.Fatal("foreign-owned intermediate accepted")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("foreign-owned intermediate was modified: %v", err)
	}
}

func TestWebhookSpoolRejectsSymlinkAndSwappedComponents(t *testing.T) {
	parent := privateSpoolTestDir(t)
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareWebhookSpoolDir(filepath.Join(link, "spool")); err == nil {
		t.Fatal("symlink intermediate accepted")
	}
	if _, err := os.Lstat(filepath.Join(target, "spool")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was modified: %v", err)
	}

	finalTarget := filepath.Join(parent, "final-target")
	if err := os.Mkdir(finalTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	finalLink := filepath.Join(parent, "final-link")
	if err := os.Symlink(finalTarget, finalLink); err != nil {
		t.Fatal(err)
	}
	if err := prepareWebhookSpoolDir(finalLink); err == nil {
		t.Fatal("symlink spool root accepted")
	}

	safe := filepath.Join(parent, "safe")
	if err := os.Mkdir(safe, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "safe-moved")
	if err := os.Rename(safe, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, safe); err != nil {
		t.Fatal(err)
	}
	if err := prepareWebhookSpoolDir(filepath.Join(safe, "spool")); err == nil {
		t.Fatal("swapped symlink intermediate accepted")
	}
	if _, err := os.Lstat(filepath.Join(moved, "spool")); !os.IsNotExist(err) {
		t.Fatalf("swapped symlink target was modified: %v", err)
	}
}

func TestWebhookSpoolRootOwnershipBoundary(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise the root ownership boundary")
	}
	parent := privateSpoolTestDir(t)
	trusted := filepath.Join(parent, "trusted")
	if err := os.Mkdir(trusted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(trusted, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := prepareWebhookSpoolDir(filepath.Join(trusted, "spool")); err != nil {
		t.Fatalf("root-owned safe intermediate rejected: %v", err)
	}

	foreign := filepath.Join(parent, "foreign")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(foreign, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := prepareWebhookSpoolDir(filepath.Join(foreign, "spool")); err == nil {
		t.Fatal("root accepted foreign-owned intermediate")
	}
}
