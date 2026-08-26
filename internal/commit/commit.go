// Package commit parses and lints Conventional Commits. It mirrors src/commit.ts.
package commit

import (
	"regexp"
	"strings"

	"github.com/openhoo/hooversion/internal/types"
)

// HeaderRE matches a conventional-commit subject: lowercase type, optional
// scope, optional "!", ": ", description.
var HeaderRE = regexp.MustCompile(`^([a-z][a-z0-9-]*)(?:\(([^()\r\n]+)\))?(!)?: (.+)$`)

var breakingFooterLinePattern = regexp.MustCompile(`^BREAKING[ -]CHANGE:\s*\S.*$`)

var breakingFooterPrefixPattern = regexp.MustCompile(`^BREAKING[ -]CHANGE:\s*`)

var ignoredSubjectPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^Merge `),
	regexp.MustCompile(`^Revert "`),
	regexp.MustCompile(`(?i)^revert: `),
	regexp.MustCompile(`^chore\(release\)!?: `),
}

// IsIgnoredSubject reports whether the subject is never released or linted.
func IsIgnoredSubject(subject string) bool {
	for _, pattern := range ignoredSubjectPatterns {
		if pattern.MatchString(subject) {
			return true
		}
	}
	return false
}

// Parse fills the structural fields of ParsedCommit from a raw commit.
// Release-type mapping (default bump map plus policy overrides) is applied by
// the planning layer, not here; policy.AllowedTypes only affects Lint.
func Parse(raw types.RawCommit, policy *types.CommitPolicy) types.ParsedCommit {
	parsed := types.ParsedCommit{
		Hash:    raw.Hash,
		Subject: raw.Subject,
		Body:    raw.Body,
		Files:   raw.Files,
	}
	match := HeaderRE.FindStringSubmatch(raw.Subject)
	if match == nil {
		parsed.Type = ""
		parsed.Description = raw.Subject
		parsed.Breaking = false
		return parsed
	}
	text, hasFooter := BreakingChange(raw.Body)
	parsed.Type = match[1]
	parsed.Scope = match[2]
	parsed.BreakingDescription = text
	parsed.Breaking = hasFooter || match[3] != ""
	parsed.Description = match[4]
	parsed.Conforms = true
	return parsed
}

// Lint returns the exact user-facing issue messages for one commit, in the
// order of src/commit.ts. Ignored subjects are never linted.
func Lint(c types.ParsedCommit, policy *types.CommitPolicy) []types.CommitLintIssue {
	if IsIgnoredSubject(c.Subject) {
		return nil
	}
	var issues []types.CommitLintIssue
	match := HeaderRE.FindStringSubmatch(c.Subject)
	if match == nil {
		return append(issues, types.CommitLintIssue{
			Message: "header must match '<type>(optional-scope)!: description'",
		})
	}
	description := match[4]
	if policy != nil && len(policy.AllowedTypes) > 0 && !contains(policy.AllowedTypes, match[1]) {
		issues = append(issues, types.CommitLintIssue{Message: "type '" + match[1] + "' is not allowed"})
	}
	if strings.TrimSpace(description) == "" {
		issues = append(issues, types.CommitLintIssue{Message: "description is required"})
	}
	if len(c.Subject) > 100 {
		issues = append(issues, types.CommitLintIssue{Message: "header must not exceed 100 characters"})
	}
	return issues
}

// Format renders "<type>(<scope>)!…: <description>"; the bang appears when
// the commit is breaking (by "!" or footer), mirroring cli.ts formatCommit
// minus the hash7 prefix that callers render themselves.
func Format(c types.ParsedCommit) string {
	scope := ""
	if c.Scope != "" {
		scope = "(" + c.Scope + ")"
	}
	bang := ""
	if c.Breaking {
		bang = "!"
	}
	return c.Type + scope + bang + ": " + c.Description
}

// BreakingChange extracts the text after a BREAKING CHANGE / BREAKING-CHANGE
// footer. The footer counts only when it is the sole body line or is preceded
// by a blank line; otherwise it is treated as ordinary prose.
func BreakingChange(body string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for index, line := range lines {
		if !breakingFooterLinePattern.MatchString(strings.TrimSpace(line)) {
			continue
		}
		if (index == 0 && len(lines) == 1) || (index > 0 && strings.TrimSpace(lines[index-1]) == "") {
			text := breakingFooterPrefixPattern.ReplaceAllString(strings.TrimSpace(line), "")
			return strings.TrimSpace(text), true
		}
	}
	return "", false
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
