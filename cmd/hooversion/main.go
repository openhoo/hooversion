// Command hooversion is the Hooversion release CLI binary.
package main

import (
	"os"

	"github.com/openhoo/hooversion/internal/app"
	"github.com/openhoo/hooversion/internal/cli"
)

// version is bound at build time via -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	cli.AppEntry = func(getenv func(string) string) error { return app.Run(getenv) }
	os.Exit(cli.Run(os.Args[1:], version))
}
