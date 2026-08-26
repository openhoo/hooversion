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

// versionPattern accepts the same inputs as src/semver.ts while additionally
// capturing the prerelease and build fragments. The optional groups mirror
// the single non-capturing (?:[-+].*)? suffix: "-", "-pre", "+build" and
// "-pre+build" are all accepted; anything else is rejected.
var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([^+]*))?(?:\+(.*))?$`)

// Parse parses a semantic version. Leading and trailing whitespace around the
// version is tolerated (the error message reports the original input).
func Parse(s string) (SemVer, error) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return SemVer{}, errors.New("Invalid semantic version: %s", s)
	}
	// Digit runs beyond int range cannot occur in practice; strconv.Atoi
	// saturates and reports ErrRange, which we deliberately ignore like
	// JavaScript's Number() coercion.
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
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
