//go:build windows

package safefs

func unsupportedDirectorySync(err error) bool { return true }
