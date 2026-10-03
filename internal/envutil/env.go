// Package envutil resolves an explicitly supplied child environment without
// falling back to unrelated credentials or CI state in the parent process.
package envutil

import (
	"os"
	"strings"
)

// Get reads the process environment only when environment is nil. An empty
// slice is an explicit empty environment; the final duplicate wins.
func Get(environment []string, name string) string {
	if environment == nil {
		return os.Getenv(name)
	}
	prefix := name + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}
