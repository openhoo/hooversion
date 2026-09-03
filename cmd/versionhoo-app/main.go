// Command versionhoo-app runs the Versionhoo GitHub App webhook server.
package main

import (
	"fmt"
	"os"

	"github.com/openhoo/hooversion/internal/app"
)

// version is bound at build time via -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	os.Exit(run(os.Getenv))
}

func run(getenv func(string) string) int {
	if err := app.Run(getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
