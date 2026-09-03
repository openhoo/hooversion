//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebhookSpoolWindowsACL(t *testing.T) {
	t.Run("ordinary newly-created path is accepted", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "webhook-spool")
		spool, err := NewWebhookSpool(dir, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := spool.Close(); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name string
		sid  string
	}{
		{name: "Everyone", sid: "*S-1-1-0"},
		{name: "Users", sid: "*S-1-5-32-545"},
	} {
		t.Run(test.name+" write is rejected", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "webhook-spool")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			grantWindowsWriteACL(t, dir, test.sid)
			if err := prepareWebhookSpoolDir(dir); err == nil {
				t.Fatalf("accepted %s write ACL", test.name)
			}
		})
	}

	t.Run("owner lock write is rejected", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "webhook-spool")
		spool, err := NewWebhookSpool(dir, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := spool.Close(); err != nil {
			t.Fatal(err)
		}
		grantWindowsDirectWriteACL(t, filepath.Join(dir, webhookSpoolOwnerName), "*S-1-1-0")
		if spool, err := NewWebhookSpool(dir, 1024); err == nil {
			_ = spool.Close()
			t.Fatal("accepted permissive owner-lock ACL")
		}
	})

	t.Run("reparse point is rejected", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "webhook-spool")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("creating a Windows reparse point is unavailable: %v", err)
		}
		if _, err := NewWebhookSpool(link, 1024); err == nil {
			t.Fatal("accepted reparse-point spool path")
		}
	})
}

func grantWindowsWriteACL(t *testing.T, path, sid string) {
	t.Helper()
	permission := sid + ":(OI)(CI)W"
	command := exec.Command("icacls", path, "/grant", permission)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("icacls cannot set the focused ACL: %v (%s)", err, output)
	}
}

func grantWindowsDirectWriteACL(t *testing.T, path, sid string) {
	t.Helper()
	command := exec.Command("icacls", path, "/grant", sid+":W")
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("icacls cannot set the focused ACL: %v (%s)", err, output)
	}
}
