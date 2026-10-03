// Package semver parses and bumps semantic versions. It mirrors src/semver.ts.
package semver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/types"
)

// SemVer holds parsed semantic-version components. Pre and Build carry the
// prerelease/build suffixes when present; Bump always drops them.
type SemVer struct {
	Major, Minor, Patch int
	Pre, Build          string
}

// String renders M.m.p plus the prerelease/build suffixes when set. A bumped
// version never carries them, matching src/semver.ts output strings.
func (v SemVer) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Prerelease and build identifiers follow SemVer syntax. Core leading zeros
// remain accepted for compatibility with the original implementation.
var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// Parse parses a semantic version. Leading and trailing whitespace around the
// version is tolerated (the error message reports the original input).
func Parse(s string) (SemVer, error) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return SemVer{}, errors.New("Invalid semantic version: %s", s)
	}
	for _, identifier := range strings.Split(m[4], ".") {
		numeric := true
		for _, char := range identifier {
			if char < '0' || char > '9' {
				numeric = false
				break
			}
		}
		if numeric && len(identifier) > 1 && identifier[0] == '0' {
			return SemVer{}, errors.New("Invalid semantic version: %s", s)
		}
	}
	major, errMajor := strconv.Atoi(m[1])
	minor, errMinor := strconv.Atoi(m[2])
	patch, errPatch := strconv.Atoi(m[3])
	if errMajor != nil || errMinor != nil || errPatch != nil {
		return SemVer{}, errors.New("Semantic version component exceeds integer range: %s", s)
	}
	return SemVer{Major: major, Minor: minor, Patch: patch, Pre: m[4], Build: m[5]}, nil
}

// Bump returns the version raised by the given release type with any
// prerelease/build suffix dropped.
func Bump(v SemVer, t types.ReleaseType) SemVer {
	switch t {
	case types.Major:
		return SemVer{Major: v.Major + 1}
	case types.Minor:
		return SemVer{Major: v.Major, Minor: v.Minor + 1}
	default:
		return SemVer{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
	}
}

// Highest returns the highest release type among ts, skipping empty entries.
// It returns "" (no release) when no entry qualifies, mirroring the undefined
// result of highestReleaseType in src/semver.ts.
func Highest(ts ...types.ReleaseType) types.ReleaseType {
	var result types.ReleaseType
	for _, t := range ts {
		if t == "" {
			continue
		}
		if t == types.Major {
			return types.Major
		}
		if t == types.Minor && result != types.Major {
			result = types.Minor
		}
		if t == types.Patch && result == "" {
			result = types.Patch
		}
	}
	return result
}

// Min raises current to at least minimum, mirroring minReleaseType.
func Min(current, minimum types.ReleaseType) types.ReleaseType {
	if h := Highest(current, minimum); h != "" {
		return h
	}
	return minimum
}

// CheckedBump prevents a release plan from wrapping a component below zero.
func CheckedBump(v SemVer, t types.ReleaseType) (SemVer, error) {
	if v.Major < 0 || v.Minor < 0 || v.Patch < 0 {
		return SemVer{}, errors.New("Invalid semantic version: %s", v.String())
	}
	if t != types.Major && t != types.Minor && t != types.Patch {
		return SemVer{}, errors.New("Invalid release type: %s", t)
	}
	next := Bump(v, t)
	if next.Major < 0 || next.Minor < 0 || next.Patch < 0 {
		return SemVer{}, errors.New("Semantic version bump exceeds integer range: %s", v.String())
	}
	return next, nil
}
