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
	os.Exit(runArgs(os.Args[1:], os.Getenv))
}

func run(getenv func(string) string) int {
	if err := app.Run(getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// Metadata commands do not require App credentials or start a listener.
func runArgs(args []string, getenv func(string) string) int {
	if len(args) == 0 {
		return run(getenv)
	}
	if len(args) == 1 {
		switch args[0] {
		case "version", "--version":
			fmt.Printf("versionhoo-app %s\n", version)
			return 0
		case "help", "--help", "-h":
			fmt.Println("Usage: versionhoo-app [--version | --help]\n\nConfigure the GitHub App server with VERSIONHOO_* environment variables.")
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "Unknown versionhoo-app arguments; use --help.")
	return 1
}
