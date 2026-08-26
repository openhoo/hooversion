// Command versionhoo-app runs the Versionhoo GitHub App webhook server.
package main

import (
	"os"

	"github.com/openhoo/hooversion/internal/app"
)

// version is bound at build time via -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	if err := app.Run(os.Getenv); err != nil {
		os.Exit(1)
	}
}
