package commit_test

import (
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/commit"
	"github.com/openhoo/hooversion/internal/types"
)

func raw(subject, body string) types.RawCommit {
	return types.RawCommit{Hash: "abc1234", Subject: subject, Body: body}
}

func TestHeaderRE(t *testing.T) {
	valid := []string{
		"feat: add printer bridge",
		"feat(ui): add theme switcher",
		"fix!: handle hotplug failure",
		"feat(api)!: change payload",
		"a: x",                       // single-char type
		"my-type: dash allowed",      // dashes in type
		"feat(a b): spaces in scope", // scope excludes only parens/CR/LF
		"feat:",                      // never matches — checked below
	}
	if !commit.HeaderRE.MatchString(valid[0]) ||
		!commit.HeaderRE.MatchString(valid[1]) ||
		!commit.HeaderRE.MatchString(valid[2]) ||
		!commit.HeaderRE.MatchString(valid[3]) ||
		!commit.HeaderRE.MatchString(valid[4]) ||
		!commit.HeaderRE.MatchString(valid[5]) ||
		!commit.HeaderRE.MatchString(valid[6]) {
		t.Fatalf("expected subjects to match header pattern")
	}
	invalid := []string{
		"Add printer bridge",
		"Feat: capital type",
		"feat:no space",
		"feat : spaced colon",
		"feat(scope with)parens): x",
		"",
		"feat:",
	}
	for _, s := range invalid {
		if commit.HeaderRE.MatchString(s) {
			t.Fatalf("%q should not match HeaderRE", s)
		}
	}
}

func TestIsIgnoredSubject(t *testing.T) {
	ignored := []string{
		"Merge branch 'main' into dev",
		"Merged PR 5", // no — must NOT be ignored; see below
		`Revert "feat: something"`,
		"revert: fix login flow",
		"REVERT: FIX LOGIN FLOW", // case-insensitive
		"chore(release): 1.2.3",
		"chore(release)!: 1.2.3",
	}
	expect := []bool{true, false, true, true, true, true, true}
	for i, s := range ignored {
		if got := commit.IsIgnoredSubject(s); got != expect[i] {
			t.Fatalf("IsIgnoredSubject(%q) = %v, want %v", s, got, expect[i])
		}
	}
	notIgnored := []string{"chore: release stuff", "chore(release-notes): bump", "Mergeable change", `Revertible`}
	for _, s := range notIgnored {
		if commit.IsIgnoredSubject(s) {
			t.Fatalf("IsIgnoredSubject(%q) = true, want false", s)
		}
	}
}

func TestBreakingChange(t *testing.T) {
	text, ok := commit.BreakingChange("BREAKING CHANGE: payload is now nested")
	if !ok || text != "payload is now nested" {
		t.Fatalf("sole line: %q %v", text, ok)
	}
	if text, ok = commit.BreakingChange("BREAKING-CHANGE: hyphen form works"); !ok || text != "hyphen form works" {
		t.Fatalf("hyphen form: %q %v", text, ok)
	}
	// Preceded by a blank line.
	if text, ok = commit.BreakingChange("details about the payload\n\nBREAKING CHANGE: payload is now nested"); !ok || text != "payload is now nested" {
		t.Fatalf("blank-line-preceded: %q %v", text, ok)
	}
	// Ported: footer directly after prose is ordinary body text.
	if _, ok = commit.BreakingChange("BREAKING CHANGE: prose\nstill ordinary body text"); ok {
		t.Fatal("footer-first-line-of-multiline must not count")
	}
	if _, ok = commit.BreakingChange("details about the payload\nBREAKING CHANGE: payload is now nested"); ok {
		t.Fatal("footer without preceding blank line must not count")
	}
	// Prefix requires non-empty description.
	if _, ok = commit.BreakingChange("\n\nBREAKING CHANGE:"); ok {
		t.Fatal("empty footer must not count")
	}
	// Indented footer after a blank line still counts; surrounding whitespace trimmed.
	if text, ok = commit.BreakingChange("body\n\n   BREAKING CHANGE:    spaced text   "); !ok || text != "spaced text" {
		t.Fatalf("indented footer: %q %v", text, ok)
	}
	// First valid occurrence wins.
	if text, ok = commit.BreakingChange("BREAKING CHANGE: bad placement\nmore\n\nBREAKING CHANGE: good"); !ok || text != "good" {
		t.Fatalf("second-footer fallback: %q %v", text, ok)
	}
	if _, ok = commit.BreakingChange(""); ok {
		t.Fatal("empty body must not count")
	}
	// CRLF bodies are handled.
	if _, ok = commit.BreakingChange("body\r\n\r\nBREAKING CHANGE: crlf works"); !ok {
		t.Fatal("crlf footer must count")
	}
}

func TestParse(t *testing.T) {
	got := commit.Parse(raw("feat(ui): add theme switcher", ""), nil)
	if got.Hash != "abc1234" || got.Subject != "feat(ui): add theme switcher" || got.Type != "feat" ||
		got.Scope != "ui" || got.Description != "add theme switcher" || !got.Conforms ||
		got.Breaking || got.BreakingDescription != "" {
		t.Fatalf("Parse conventional = %+v", got)
	}

	// Bang marks breaking without a description.
	got = commit.Parse(raw("feat!: change config format", ""), nil)
	if !got.Breaking || got.Scope != "" || got.BreakingDescription != "" {
		t.Fatalf("Parse bang = %+v", got)
	}

	// Footer marks breaking and carries its text.
	got = commit.Parse(raw("feat(api): change payload", "BREAKING CHANGE: payload is now nested"), nil)
	if !got.Breaking || got.BreakingDescription != "payload is now nested" {
		t.Fatalf("Parse footer = %+v", got)
	}

	// Footer prose does not mark breaking.
	got = commit.Parse(raw("feat(api): mention a phrase", "BREAKING CHANGE: prose\nstill ordinary body text"), nil)
	if got.Breaking || got.BreakingDescription != "" {
		t.Fatalf("Parse footer-prose = %+v", got)
	}

	// Non-conforming subject keeps the subject as description.
	got = commit.Parse(raw("Add printer bridge", ""), nil)
	if got.Conforms || got.Type != "" || got.Scope != "" || got.Description != "Add printer bridge" || got.Breaking {
		t.Fatalf("Parse non-conventional = %+v", got)
	}

	// Raw fields are preserved.
	in := types.RawCommit{Hash: "deadbeef", Subject: "fix: repair", Body: "note", Files: []string{"a.go"}}
	got = commit.Parse(in, nil)
	if got.Body != "note" || len(got.Files) != 1 || got.Files[0] != "a.go" {
		t.Fatalf("Parse raw passthrough = %+v", got)
	}

	// Release-commit subjects parse structurally even though they are ignored.
	got = commit.Parse(raw("chore(release): app 1.2.3", ""), &types.CommitPolicy{})
	if !got.Conforms || got.Type != "chore" || got.Scope != "release" {
		t.Fatalf("Parse release subject = %+v", got)
	}
}

func lintMessages(t *testing.T, c types.ParsedCommit, policy *types.CommitPolicy) []string {
	t.Helper()
	var msgs []string
	for _, issue := range commit.Lint(c, policy) {
		msgs = append(msgs, issue.Message)
	}
	return msgs
}

func TestLint(t *testing.T) {
	// Ported: lints invalid headers.
	if msgs := lintMessages(t, commit.Parse(raw("Add printer bridge", ""), nil), nil); len(msgs) != 1 ||
		msgs[0] != "header must match '<type>(optional-scope)!: description'" {
		t.Fatalf("invalid header issues = %v", msgs)
	}
	if msgs := lintMessages(t, commit.Parse(raw("feat: add printer bridge", ""), nil), nil); len(msgs) != 0 {
		t.Fatalf("valid header issues = %v", msgs)
	}

	// Ignored subjects are skipped entirely.
	if msgs := lintMessages(t, commit.Parse(raw("chore(release): 1.2.3", ""), nil), nil); len(msgs) != 0 {
		t.Fatalf("ignored subject issues = %v", msgs)
	}

	// Allowed-types policy.
	policy := &types.CommitPolicy{AllowedTypes: []string{"feat", "docs"}}
	if msgs := lintMessages(t, commit.Parse(raw("docs: publish guide", ""), nil), policy); len(msgs) != 0 {
		t.Fatalf("allowed type issues = %v", msgs)
	}
	msgs := lintMessages(t, commit.Parse(raw("chore: tidy", ""), nil), policy)
	if len(msgs) != 1 || msgs[0] != "type 'chore' is not allowed" {
		t.Fatalf("disallowed type issues = %v", msgs)
	}

	// Description is required (whitespace-only descriptions fail).
	if msgs := lintMessages(t, commit.Parse(raw("feat:   ", ""), nil), nil); len(msgs) != 1 ||
		msgs[0] != "description is required" {
		t.Fatalf("description issues = %v", msgs)
	}

	// Exactly 100 characters pass, 101 fail.
	long := strings.Repeat("a", 94) // "feat: " + 94 = 100
	if msgs := lintMessages(t, commit.Parse(raw("feat: "+long, ""), nil), nil); len(msgs) != 0 {
		t.Fatalf("100-char subject issues = %v", msgs)
	}
	msgs = lintMessages(t, commit.Parse(raw("feat: "+long+"a", ""), nil), nil)
	if len(msgs) != 1 || msgs[0] != "header must not exceed 100 characters" {
		t.Fatalf("101-char subject issues = %v", msgs)
	}

	// Issue order follows src/commit.ts: allowedTypes, description, length.
	policy2 := &types.CommitPolicy{AllowedTypes: []string{"fix"}}
	msgs = lintMessages(t, commit.Parse(raw("chore:   ", ""), nil), policy2)
	want := []string{"type 'chore' is not allowed", "description is required"}
	if len(msgs) != 2 || msgs[0] != want[0] || msgs[1] != want[1] {
		t.Fatalf("combined issues = %v", msgs)
	}

	// Empty policy behaves like nil.
	if msgs := lintMessages(t, commit.Parse(raw("chore: tidy", ""), nil), &types.CommitPolicy{}); len(msgs) != 0 {
		t.Fatalf("empty policy issues = %v", msgs)
	}
}

func TestFormat(t *testing.T) {
	if got := commit.Format(commit.Parse(raw("feat(ui): add theme switcher", ""), nil)); got != "feat(ui): add theme switcher" {
		t.Fatalf("Format scoped = %q", got)
	}
	if got := commit.Format(commit.Parse(raw("fix: handle hotplug failure", ""), nil)); got != "fix: handle hotplug failure" {
		t.Fatalf("Format plain = %q", got)
	}
	if got := commit.Format(commit.Parse(raw("feat(api)!: change payload", ""), nil)); got != "feat(api)!: change payload" {
		t.Fatalf("Format bang = %q", got)
	}
	// Footer-breaking commits render the bang even though the subject lacks it,
	// mirroring cli.ts formatCommit.
	got := commit.Parse(raw("feat(api): change payload", "\n\nBREAKING CHANGE: nested"), nil)
	if got := commit.Format(got); got != "feat(api)!: change payload" {
		t.Fatalf("Format footer-breaking = %q", got)
	}
}
