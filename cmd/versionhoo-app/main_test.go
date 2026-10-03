package main

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestRunReportsStartupConfigurationError(t *testing.T) {
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	code := run(func(string) string { return "" })
	if code != 1 {
		t.Fatalf("run code = %d, want 1", code)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want := "VERSIONHOO_APP_ID or HOOVERSION_APP_ID is required.\n"
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestVersionDoesNotRequireAppConfiguration(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old; r.Close() }()
	code := runArgs([]string{"--version"}, func(string) string { t.Fatal("version tried to read App credentials"); return "" })
	w.Close()
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || string(output) != "versionhoo-app "+version+"\n" {
		t.Fatalf("code=%d output=%q", code, output)
	}
}
