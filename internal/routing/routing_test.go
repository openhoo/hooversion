package routing

import (
	"reflect"
	"testing"

	"github.com/openhoo/hooversion/internal/types"
)

func commit(hash, scope string, files ...string) types.ParsedCommit {
	return types.ParsedCommit{Hash: hash, Scope: scope, Files: files}
}

func testConfig() *types.NormalizedConfig {
	return &types.NormalizedConfig{
		Packages: []types.NormalizedPackageConfig{
			{Name: "one", Path: "packages/one", Scopes: []string{"one-scope", "shared"}},
			{Name: "two", Path: "packages/two", Scopes: []string{"two-scope"}},
		},
	}
}

func TestDirectAffectedByScope(t *testing.T) {
	config := testConfig()
	got := DirectAffected(config, []types.ParsedCommit{
		commit("a", "one-scope"),
		commit("b", "two-scope"),
		commit("c", "one"),
		commit("d", "one-scope,two-scope"),
	})
	if names := keys(got); !reflect.DeepEqual(names, []string{"one", "two"}) {
		t.Fatalf("affected packages = %v, want [one two]", names)
	}
	if len(got["one"]) != 3 || len(got["two"]) != 2 {
		t.Fatalf("counts one=%d two=%d, want 3/2", len(got["one"]), len(got["two"]))
	}
	// A commit attributed through both scope lists lands once per package.
	if got["one"][2].Hash != "d" || got["two"][1].Hash != "d" {
		t.Fatalf("multi-scope commit routing wrong: %+v", got)
	}
}

func TestScopeMatchIsCaseSensitiveAndOwnNameCounts(t *testing.T) {
	config := &types.NormalizedConfig{
		Packages: []types.NormalizedPackageConfig{{Name: "app", Path: ".", Scopes: []string{"core"}}},
	}
	got := DirectAffected(config, []types.ParsedCommit{
		commit("a", "Core"), // case differs -> no match
		commit("b", "app"),  // own name matches
	})
	if len(got["app"]) != 1 || got["app"][0].Hash != "b" {
		t.Fatalf("case-sensitive scope routing wrong: %+v", got)
	}
}

func TestFileOwnershipAndRootExclusion(t *testing.T) {
	config := testConfig()
	rootConfig := config
	rootConfig.Packages = append(rootConfig.Packages,
		types.NormalizedPackageConfig{Name: "root", Path: "."})

	got := DirectAffected(rootConfig, []types.ParsedCommit{
		commit("a", "", "packages/one/index.ts"),
		commit("b", "", "packages/two/x.go"),
		commit("c", "", "README.md"),
		commit("d", "", "packages/one/deep/nested.txt", "docs/other.md"),
	})
	if len(got["one"]) != 2 || got["one"][0].Hash != "a" || got["one"][1].Hash != "d" {
		t.Fatalf("one routing wrong: %+v", got["one"])
	}
	if len(got["two"]) != 1 || got["two"][0].Hash != "b" {
		t.Fatalf("two routing wrong: %+v", got["two"])
	}
	// Root owns only files outside every sub-package; a commit touching both a
	// sub-package file and an outside file reaches both packages.
	if len(got["root"]) != 2 || got["root"][0].Hash != "c" || got["root"][1].Hash != "d" {
		t.Fatalf("root routing wrong: %+v", got["root"])
	}
}

func TestUnmatchedCommitsProduceNoEntries(t *testing.T) {
	config := testConfig()
	got := DirectAffected(config, []types.ParsedCommit{commit("a", "unknown", "outside.txt")})
	if len(got) != 0 {
		t.Fatalf("expected no affected packages, got %v", keys(got))
	}
}

func TestDuplicateAttributionCollapsesToOneEntryPerPackage(t *testing.T) {
	config := &types.NormalizedConfig{
		Packages: []types.NormalizedPackageConfig{
			{Name: "app", Path: ".", Scopes: []string{"app"}},
		},
	}
	got := DirectAffected(config, []types.ParsedCommit{commit("a", "app", "index.ts")})
	if len(got["app"]) != 1 {
		t.Fatalf("scope+file double attribution not collapsed: %+v", got["app"])
	}
}

func TestEmptyScopeAndFiles(t *testing.T) {
	config := testConfig()
	got := DirectAffected(config, []types.ParsedCommit{commit("a", "")})
	if len(got) != 0 {
		t.Fatalf("empty commit should affect nothing, got %v", keys(got))
	}
}

func keys(m map[string][]types.ParsedCommit) []string {
	out := make([]string, 0, len(m))
	for _, name := range []string{"one", "two", "root", "app"} {
		if _, ok := m[name]; ok {
			out = append(out, name)
		}
	}
	return out
}
