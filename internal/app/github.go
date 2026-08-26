// This file mirrors src/app-github.ts: check-run lifecycle over the landed
// githubapi client and the outcome-to-check-result mapping (including
// failure-summary truncation to 60000 chars). Check failures are warn-only.
package app

import (
	"fmt"
	"log"

	"github.com/openhoo/hooversion/internal/githubapi"
)

const maxCheckSummaryLength = 60000

// CheckConclusion mirrors CheckConclusion.
type CheckConclusion string

const (
	CheckConclusionSuccess CheckConclusion = "success"
	CheckConclusionFailure CheckConclusion = "failure"
	CheckConclusionNeutral CheckConclusion = "neutral"
)

// CheckResult carries conclusion/title/summary for completion.
type CheckResult struct {
	Conclusion CheckConclusion
	Title      string
	Summary    string
}

var warnf = func(format string, args ...any) { log.Printf(format, args...) }

// mintToken exchanges the App JWT for an installation token; a package-level
// seam for tests. Production uses githubapi.MintInstallationToken.
var mintToken = githubapi.MintInstallationToken

// newCheckRunClient builds the check-run API client; test seam as well.
var newCheckRunClient = func(apiURL, token string, trustedApiURLs []string) (*githubapi.Client, error) {
	trusted, err := ValidateGitHubApiURL(apiURL, trustedApiURLs)
	if err != nil {
		return nil, err
	}
	return githubapi.New(trusted, token), nil
}

// createReleaseCheckRun mirrors createReleaseCheckRun: starts an in_progress
// check run with the canonical started output.
func createReleaseCheckRun(client *githubapi.Client, repository, headSha string) (int64, error) {
	return client.CreateCheckRun(repository, githubapi.CheckRunInput{
		Name:          githubapi.CheckRunName,
		HeadSHA:       headSha,
		Status:        githubapi.CheckRunStatusInProgress,
		OutputTitle:   "Versionhoo release started",
		OutputSummary: "Versionhoo accepted this workflow run and started release processing.",
	})
}

// completeReleaseCheckRun mirrors completeReleaseCheckRun.
func completeReleaseCheckRun(client *githubapi.Client, repository string, checkRunID int64, check CheckResult) error {
	return client.CompleteCheckRun(repository, checkRunID, githubapi.CheckRunResult{
		Status:        githubapi.CheckRunStatusCompleted,
		Conclusion:    string(check.Conclusion),
		OutputTitle:   check.Title,
		OutputSummary: check.Summary,
	})
}

// ReleaseCheckResult mirrors releaseCheckResult.
func ReleaseCheckResult(outcome Outcome) CheckResult {
	if outcome.Outcome == "stale" {
		summary := outcome.Message
		if summary == "" {
			summary = "The release branch moved after this workflow run completed."
		}
		return CheckResult{
			Conclusion: CheckConclusionNeutral,
			Title:      "Versionhoo skipped a stale workflow run",
			Summary:    summary,
		}
	}
	if !outcome.Published {
		return CheckResult{
			Conclusion: CheckConclusionNeutral,
			Title:      "Versionhoo found no release",
			Summary:    "No release-worthy commits were found for this workflow run.",
		}
	}
	lines := ""
	for i, release := range outcome.Releases {
		if i > 0 {
			lines += "\n"
		}
		lines += fmt.Sprintf("- %s %s (%s)", release.Name, release.Version, release.Tag)
	}
	summary := lines
	if summary == "" {
		summary = "Versionhoo published releases."
	}
	return CheckResult{
		Conclusion: CheckConclusionSuccess,
		Title:      "Versionhoo published releases",
		Summary:    summary,
	}
}

// ReleaseFailureCheckResult mirrors releaseFailureCheckResult.
func ReleaseFailureCheckResult(err error) CheckResult {
	return CheckResult{
		Conclusion: CheckConclusionFailure,
		Title:      "Versionhoo release failed",
		Summary:    truncateSummary(err.Error(), maxCheckSummaryLength),
	}
}

// truncateSummary mirrors truncate: an ellipsis replaces the tail past the cap.
func truncateSummary(value string, maxLength int) string {
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	return string(runes[:maxLength-3]) + "..."
}
