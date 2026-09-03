// Command hooversion is the Hooversion release CLI binary.
package main

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/openhoo/hooversion/internal/app"
	"github.com/openhoo/hooversion/internal/cli"
	"github.com/openhoo/hooversion/internal/semver"
)

// version is bound at build time via -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	cli.AppEntry = func(getenv func(string) string) error { return app.Run(getenv) }
	os.Exit(cli.Run(os.Args[1:], effectiveVersion()))
}

func effectiveVersion() string {
	var info *debug.BuildInfo
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		info = buildInfo
	}
	return resolveVersion(version, info)
}

func resolveVersion(linked string, info *debug.BuildInfo) string {
	if normalized, ok := normalizeVersion(linked); ok && linked != "dev" {
		return normalized
	}
	if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		if normalized, ok := normalizeVersion(info.Main.Version); ok {
			return normalized
		}
	}
	return "dev"
}

func normalizeVersion(raw string) (string, bool) {
	candidate := strings.TrimSpace(raw)
	candidate = strings.TrimPrefix(candidate, "v")
	parsed, err := semver.Parse(candidate)
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}
