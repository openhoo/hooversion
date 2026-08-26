package semver_test

import (
	"testing"

	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/semver"
	"github.com/openhoo/hooversion/internal/types"
)

// Ports assertions from tests/semver.test.ts plus contract §5 edge cases.
func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want semver.SemVer
	}{
		{"1.2.3", semver.SemVer{Major: 1, Minor: 2, Patch: 3}},
		{"1.2.3-alpha.1", semver.SemVer{Major: 1, Minor: 2, Patch: 3, Pre: "alpha.1"}},
		{"1.2.3+build.5", semver.SemVer{Major: 1, Minor: 2, Patch: 3, Build: "build.5"}},
		{"1.2.3-a+b", semver.SemVer{Major: 1, Minor: 2, Patch: 3, Pre: "a", Build: "b"}},
		{"01.02.03", semver.SemVer{Major: 1, Minor: 2, Patch: 3}},
		{" 1.2.3 ", semver.SemVer{Major: 1, Minor: 2, Patch: 3}},
	}
	for _, c := range cases {
		got, err := semver.Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q) unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "1.2", "v1.2.3", "1.2.3.4", "abc", "1.2.x"} {
		if _, err := semver.Parse(bad); err == nil {
			t.Fatalf("Parse(%q) should fail", bad)
		} else if exit, ok := err.(*errors.ExitError); !ok || exit.Code != 1 {
			t.Fatalf("Parse(%q) error type/code mismatch: %v", bad, err)
		}
	}
	_, err := semver.Parse("v1.2.3")
	if err == nil || err.Error() != "Invalid semantic version: v1.2.3" {
		t.Fatalf("exact error message mismatch: %v", err)
	}
}

func TestParseString(t *testing.T) {
	v, _ := semver.Parse("1.2.3-rc.1+b7")
	if v.String() != "1.2.3-rc.1+b7" {
		t.Fatalf("String() = %q", v.String())
	}
	p, _ := semver.Parse("4.5.6")
	if p.String() != "4.5.6" {
		t.Fatalf("String() = %q", p.String())
	}
}

func TestBump(t *testing.T) {
	// Ported: bumps versions by release type.
	base := semver.SemVer{Major: 1, Minor: 2, Patch: 3}
	if got := semver.Bump(base, types.Patch); got.String() != "1.2.4" {
		t.Fatalf("patch bump = %s", got)
	}
	if got := semver.Bump(base, types.Minor); got.String() != "1.3.0" {
		t.Fatalf("minor bump = %s", got)
	}
	if got := semver.Bump(base, types.Major); got.String() != "2.0.0" {
		t.Fatalf("major bump = %s", got)
	}
	// Prerelease and build are dropped by bump.
	suffixed := semver.SemVer{Major: 1, Minor: 2, Patch: 3, Pre: "rc.1", Build: "b7"}
	got := semver.Bump(suffixed, types.Minor)
	if got != (semver.SemVer{Major: 1, Minor: 3}) || got.String() != "1.3.0" {
		t.Fatalf("suffix not dropped: %+v (%s)", got, got)
	}
}

func TestHighest(t *testing.T) {
	// Ported: selects the highest release type.
	if got := semver.Highest(types.Patch, types.Minor); got != types.Minor {
		t.Fatalf("Highest(patch, minor) = %q", got)
	}
	if got := semver.Highest(types.Patch, types.Major, types.Minor); got != types.Major {
		t.Fatalf("Highest(patch, major, minor) = %q", got)
	}
	// Ported: undefined input yields no result (zero value in Go).
	if got := semver.Highest(types.ReleaseType("")); got != "" {
		t.Fatalf("Highest(empty) = %q", got)
	}
	if got := semver.Highest(); got != "" {
		t.Fatalf("Highest() = %q", got)
	}
}

func TestMin(t *testing.T) {
	if got := semver.Min(types.Patch, types.Patch); got != types.Patch {
		t.Fatalf("Min(patch, patch) = %q", got)
	}
	if got := semver.Min(types.Major, types.Patch); got != types.Major {
		t.Fatalf("Min(major, patch) = %q", got)
	}
	if got := semver.Min(types.Minor, types.Major); got != types.Major {
		t.Fatalf("Min(minor, major) = %q", got)
	}
	if got := semver.Min(types.ReleaseType(""), types.Minor); got != types.Minor {
		t.Fatalf("Min(empty, minor) = %q", got)
	}
	if got := semver.Min("", ""); got != types.ReleaseType("") {
		t.Fatalf("Min(empty, empty) = %q", got)
	}
}
