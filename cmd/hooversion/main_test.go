package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersionPrefersValidLinkedVersion(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v2.4.6"}}
	if got := resolveVersion("v1.2.3", info); got != "1.2.3" {
		t.Fatalf("resolveVersion = %q, want 1.2.3", got)
	}
}

func TestResolveVersionUsesModuleMetadataForDevBuild(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v2.4.6-0.20260902000000-abcdef123456"}}
	if got := resolveVersion("dev", info); got != "2.4.6-0.20260902000000-abcdef123456" {
		t.Fatalf("resolveVersion = %q, want normalized module version", got)
	}
}

func TestResolveVersionFallsBackToDevForMalformedMetadata(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	if got := resolveVersion("not-a-version", info); got != "dev" {
		t.Fatalf("resolveVersion = %q, want dev", got)
	}
}

func TestNormalizeVersionAcceptsOneLeadingV(t *testing.T) {
	if got, ok := normalizeVersion(" v3.0.0 "); !ok || got != "3.0.0" {
		t.Fatalf("normalizeVersion = %q, %v", got, ok)
	}
}
