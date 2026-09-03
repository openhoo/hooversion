// Package cli implements the hooversion command-line surface. Behavior
// mirrors src/cli.ts 1:1: command dispatch, strict flag parsing, and the
// exact user-facing output strings. The GitHub workflow templates ported
// from src/workflow.ts live in workflows.go.
//
// The Versionhoo app server is reached through the AppEntry seam, which
// cmd/hooversion wires to internal/app; this package never imports
// internal/app.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/commit"
	"github.com/openhoo/hooversion/internal/config"
	"github.com/openhoo/hooversion/internal/doctor"
	hverrors "github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/plan"
	"github.com/openhoo/hooversion/internal/release"
	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/types"
	"github.com/openhoo/hooversion/internal/verifyrelease"
)

// AppEntry starts the Versionhoo app server with an env lookup function.
// cmd/hooversion assigns it from internal/app before calling Run; when nil,
// `hooversion app` fails with a dedicated error.
var AppEntry func(getenv func(string) string) error

var runVerifyRelease = verifyrelease.Verify

// cliFlags is the parsed flag set of one invocation.
type cliFlags struct {
	values      map[string]string
	booleans    map[string]bool
	positionals []string
}

func (f *cliFlags) value(name string) string { return f.values[name] }

func (f *cliFlags) hasValue(name string) bool {
	_, ok := f.values[name]
	return ok
}

func (f *cliFlags) boolean(name string) bool { return f.booleans[name] }

// commandSpec declares the accepted options of one command.
type commandSpec struct {
	values   []string
	booleans []string
}

var commandOptions = map[string]commandSpec{
	"init": {
		values:   []string{"action-owner-repo", "action-ref", "hooversion-version"},
		booleans: []string{"force", "no-workflow"},
	},
	"lint":    {values: []string{"edit", "from", "to"}, booleans: []string{"last"}},
	"plan":    {values: []string{"config"}, booleans: []string{}},
	"release": {values: []string{"config"}, booleans: []string{"dry-run", "no-push", "no-github"}},
	"verify-release": {
		values: []string{
			"repository", "tag", "api-url", "checksums", "output",
			"signature-identity", "signature-issuer", "signer-workflow", "source-ref",
			"verifier-id", "policy-uri",
		},
		booleans: []string{
			"require-sbom", "require-license", "require-signed-tag",
			"require-signatures", "require-attestations",
		},
	},
	"doctor":    {values: []string{"config"}, booleans: []string{}},
	"migrate":   {values: []string{}, booleans: []string{}}, // accepts one optional positional path
	"app":       {values: []string{}, booleans: []string{}},
	"help":      {values: []string{}, booleans: []string{}},
	"--help":    {values: []string{}, booleans: []string{}},
	"-h":        {values: []string{}, booleans: []string{}},
	"version":   {values: []string{}, booleans: []string{}},
	"--version": {values: []string{}, booleans: []string{}},
	"-v":        {values: []string{}, booleans: []string{}},
}

// Run executes one CLI invocation and returns the process exit code.
// *errors.ExitError exits with its code; every other error prints and exits 1.
func Run(args []string, version string) int {
	command := "help"
	var rest []string
	if len(args) > 0 {
		command = args[0]
		rest = args[1:]
	}

	flags, err := parseFlags(command, rest)
	if err != nil {
		return reportError(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return reportError(err)
	}

	switch command {
	case "init":
		err = initCommand(cwd, flags, version)
	case "lint":
		err = lintCommand(cwd, flags)
	case "plan":
		err = planCommand(cwd, flags)
	case "release":
		err = releaseCommand(cwd, flags)
	case "verify-release":
		err = verifyReleaseCommand(cwd, flags)
	case "doctor":
		err = doctorCommand(cwd, flags)
	case "migrate":
		err = migrateCommand(cwd, flags)
	case "app":
		if AppEntry == nil {
			err = hverrors.New("app command requires the versionhoo-app binary")
		} else {
			err = AppEntry(os.Getenv)
		}
	case "help", "--help", "-h":
		printHelp()
		return 0
	case "version", "--version", "-v":
		printVersion(version)
		return 0
	default:
		err = hverrors.New("Unknown command: %s", command)
	}
	if err != nil {
		return reportError(err)
	}
	return 0
}

// reportError prints err the way src/cli.ts main().catch does: ExitError
// messages alone on stderr with their exit code, everything else as a plain
// failure with exit code 1.
func reportError(err error) int {
	var exitErr *hverrors.ExitError
	if errors.As(err, &exitErr) {
		fmt.Fprintln(os.Stderr, exitErr.Msg)
		return exitErr.Code
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}

// parseFlags rejects unknown options, duplicates, boolean-with-inline-value,
// missing/dash-leading values, and positionals exactly like src/cli.ts.
// Only `migrate` accepts positionals (one optional legacy-config path).
func parseFlags(command string, args []string) (*cliFlags, error) {
	spec, ok := commandOptions[command]
	if !ok {
		return nil, hverrors.New("Unknown command: %s", command)
	}

	flags := &cliFlags{values: map[string]string{}, booleans: map[string]bool{}}
	valueOptions := map[string]bool{}
	for _, name := range spec.values {
		valueOptions[name] = true
	}
	booleanOptions := map[string]bool{}
	for _, name := range spec.booleans {
		booleanOptions[name] = true
	}

	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "--") {
			flags.positionals = append(flags.positionals, arg)
			continue
		}

		raw := arg[2:]
		name := raw
		var inlineValue string
		hasInline := false
		if separator := strings.Index(raw, "="); separator != -1 {
			name = raw[:separator]
			inlineValue = raw[separator+1:]
			hasInline = true
		}
		if name == "" || (!valueOptions[name] && !booleanOptions[name]) {
			shown := name
			if shown == "" {
				shown = raw
			}
			return nil, hverrors.New("Unknown option for %s: --%s", command, shown)
		}
		if _, seenValue := flags.values[name]; seenValue || flags.booleans[name] {
			return nil, hverrors.New("Option may only be specified once: --%s", name)
		}

		if valueOptions[name] {
			missing := !hasInline && index+1 >= len(args)
			value := inlineValue
			if !hasInline && !missing {
				value = args[index+1]
			}
			if missing || strings.HasPrefix(value, "-") || strings.TrimSpace(value) == "" {
				return nil, hverrors.New("Option requires a non-empty value: --%s", name)
			}
			if !hasInline {
				index++
			}
			flags.values[name] = value
			continue
		}

		if hasInline {
			return nil, hverrors.New("Boolean option does not accept a value: --%s", name)
		}
		flags.booleans[name] = true
	}

	if len(flags.positionals) > 0 && command != "migrate" {
		return nil, hverrors.New("Unexpected positional argument: %s", flags.positionals[0])
	}
	return flags, nil
}

// initCommand mirrors src/cli.ts initCommand: config-existence guards,
// package detection, workflow generation, then default config writing.
func initCommand(cwd string, flags *cliFlags, version string) error {
	force := flags.boolean("force")

	var existingConfigs []string
	for _, name := range []string{
		"hooversion.yaml",
		".hooversion.yaml",
		"hooversion.yml",
		".hooversion.yml",
		"hooversion.config.json",
		"hooversion.json",
		"hooversion.config.ts",
		"hooversion.config.mjs",
		"hooversion.config.js",
		"hooversion.config.cjs",
	} {
		path := filepath.Join(cwd, name)
		if _, err := os.Stat(path); err == nil {
			existingConfigs = append(existingConfigs, path)
		}
	}
	selectedConfig, err := config.FindPath(cwd)
	if err != nil {
		var legacy *config.LegacyConfigError
		if errors.As(err, &legacy) {
			selectedConfig = legacy.Path
		} else {
			return err
		}
	}
	if len(existingConfigs) > 0 && !force {
		return hverrors.New("Hooversion config already exists. Use --force to overwrite.")
	}
	if force && len(existingConfigs) > 1 {
		return hverrors.New("Multiple Hooversion configs exist; remove duplicate config files before using --force.")
	}

	packages, err := config.DetectPackages(cwd)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return hverrors.New("Could not detect package.json, Cargo.toml, pyproject.toml, or version.")
	}

	var workflowPaths []string
	if !flags.boolean("no-workflow") {
		workflowPaths, err = writeGitHubWorkflows(cwd, workflowOptions{
			actionOwnerRepo:   flags.value("action-owner-repo"),
			actionRef:         flags.value("action-ref"),
			hooversionVersion: flags.value("hooversion-version"),
			force:             force,
			cliVersion:        version,
			packages:          packages,
		})
		if err != nil {
			return err
		}
	}

	configPath, err := config.WriteDefault(cwd)
	if err != nil {
		return err
	}
	if force && selectedConfig != "" && selectedConfig != configPath {
		if err := os.Remove(selectedConfig); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stdout, "Wrote %s\n", configPath)
	for _, workflowPath := range workflowPaths {
		fmt.Fprintf(os.Stdout, "Wrote %s\n", workflowPath)
	}
	return nil
}

// lintCommand lints the commits selected by exactly one selector.
func lintCommand(cwd string, flags *cliFlags) error {
	commits, err := readLintCommits(cwd, flags)
	if err != nil {
		return err
	}

	issueCount := 0
	for _, raw := range commits {
		parsed := commit.Parse(raw, nil)
		for _, issue := range commit.Lint(parsed, nil) {
			hash := ""
			if parsed.Hash != "" {
				hash = hash7(parsed.Hash) + " "
			}
			fmt.Fprintf(os.Stderr, "%s%s\n", hash, parsed.Subject)
			fmt.Fprintf(os.Stderr, "  %s\n", issue.Message)
			issueCount++
		}
	}
	if issueCount > 0 {
		return hverrors.New("Commit lint failed with %d issue(s).", issueCount)
	}

	plural := "s"
	if len(commits) == 1 {
		plural = ""
	}
	fmt.Fprintf(os.Stdout, "Validated %d commit%s.\n", len(commits), plural)
	return nil
}

// readLintCommits resolves --last | --edit <file> | --from <ref> [--to <ref>].
func readLintCommits(cwd string, flags *cliFlags) ([]types.RawCommit, error) {
	selectors := 0
	if flags.hasValue("edit") {
		selectors++
	}
	if flags.boolean("last") {
		selectors++
	}
	if flags.hasValue("from") || flags.hasValue("to") {
		selectors++
	}
	if selectors != 1 {
		return nil, hverrors.New("lint requires exactly one selector: --last, --edit <file>, or --from <ref> [--to <ref>].")
	}

	if editPath := flags.value("edit"); editPath != "" {
		message, err := os.ReadFile(editPath)
		if err != nil {
			return nil, err
		}
		lines := splitLines(string(message))
		subject := ""
		var body []string
		if len(lines) > 0 {
			subject = lines[0]
			body = lines[1:]
		}
		return []types.RawCommit{{Hash: "", Subject: subject, Body: strings.Join(body, "\n"), Files: []string{}}}, nil
	}

	if flags.boolean("last") {
		last, err := git.LastCommit(cwd)
		if err != nil {
			return nil, err
		}
		return []types.RawCommit{last}, nil
	}

	to := "HEAD"
	if flags.hasValue("to") {
		to = flags.value("to")
	}
	from := flags.value("from")
	if from == "" {
		return nil, hverrors.New("--to requires --from.")
	}
	if err := validateGitRef(cwd, from, "from"); err != nil {
		return nil, err
	}
	if err := validateGitRef(cwd, to, "to"); err != nil {
		return nil, err
	}
	return git.Commits(cwd, from, to)
}

// validateGitRef probes `git rev-parse --verify --end-of-options <ref>^{commit}`.
func validateGitRef(cwd, ref, name string) error {
	if strings.TrimSpace(ref) == "" {
		return hverrors.New("--%s requires a non-empty git ref.", name)
	}
	resolved, _ := probeGit(cwd, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if strings.TrimSpace(resolved) == "" {
		return hverrors.New("Invalid git ref for --%s: %s", name, ref)
	}
	return nil
}

// probeGit runs git tolerating failure and returns raw stdout, mirroring the
// allowFailure `git()` helper of src/git.ts.
func probeGit(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	return string(out), err
}

// splitLines splits on /\r?\n/.
func splitLines(message string) []string {
	return strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
}

func planCommand(cwd string, flags *cliFlags) error {
	cfg, err := config.Load(cwd, flags.value("config"))
	if err != nil {
		return err
	}
	p, err := buildPlan(cwd, cfg)
	if err != nil {
		return err
	}
	printPlan(p)
	if len(p.UnmatchedCommits) > 0 {
		return hverrors.New("Plan contains unmatched release-worthy commits.")
	}
	return nil
}

func releaseCommand(cwd string, flags *cliFlags) error {
	cfg, err := config.Load(cwd, flags.value("config"))
	if err != nil {
		return err
	}
	p, err := buildPlan(cwd, cfg)
	if err != nil {
		return err
	}
	dryRun := flags.boolean("dry-run")
	execution, err := release.Execute(cwd, cfg, p, release.Options{
		DryRun:      dryRun,
		NoPushSet:   flags.boolean("no-push"),
		Push:        false,
		NoGitHubSet: flags.boolean("no-github"),
		GitHub:      false,
	})
	if err != nil {
		return err
	}
	printPlan(execution.Plan)
	switch {
	case dryRun:
		fmt.Fprintln(os.Stdout, "Dry run complete; no files, commits, tags, or releases were created.")
	case execution.Published:
		fmt.Fprintln(os.Stdout, "Release complete.")
	default:
		fmt.Fprintln(os.Stdout, "No release needed.")
	}
	return nil
}

func verifyReleaseCommand(cwd string, flags *cliFlags) error {
	repository := flags.value("repository")
	if repository == "" {
		var err error
		repository, err = git.OriginRepository(cwd)
		if err != nil {
			return err
		}
		if repository == "" {
			return hverrors.New("verify-release requires --repository <owner/name> outside a recognized GitHub checkout.")
		}
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	result, err := runVerifyRelease(context.Background(), verifyrelease.Options{
		Repository:          repository,
		Tag:                 flags.value("tag"),
		APIURL:              flags.value("api-url"),
		Token:               token,
		ChecksumsAsset:      flags.value("checksums"),
		RequireSBOM:         flags.boolean("require-sbom"),
		RequireLicense:      flags.boolean("require-license"),
		RequireSignedTag:    flags.boolean("require-signed-tag"),
		RequireSignatures:   flags.boolean("require-signatures"),
		SignatureIdentity:   flags.value("signature-identity"),
		SignatureIssuer:     flags.value("signature-issuer"),
		RequireAttestations: flags.boolean("require-attestations"),
		SignerWorkflow:      flags.value("signer-workflow"),
		SourceRef:           flags.value("source-ref"),
		VerifierID:          flags.value("verifier-id"),
		PolicyURI:           flags.value("policy-uri"),
	})
	if err != nil {
		return err
	}
	if outputPath := flags.value("output"); outputPath != "" {
		data, err := verifyrelease.MarshalStatement(result.Statement)
		if err != nil {
			return err
		}
		if err := writeExclusive(outputPath, data); err != nil {
			return fmt.Errorf("write verification statement: %w", err)
		}
		fmt.Fprintf(os.Stdout, "Wrote VSA to %s.\n", outputPath)
	}
	fmt.Fprintf(os.Stdout, "Verified %d release artifacts for %s@%s at commit %s.\n",
		len(result.Statement.Subject), result.Repository, result.Tag, result.TagCommit)
	return nil
}

func writeExclusive(path string, data []byte) (err error) {
	file, err := safefs.CreateExclusive(path, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	keep = true
	return nil
}

func doctorCommand(cwd string, flags *cliFlags) error {
	cfg, err := config.Load(cwd, flags.value("config"))
	if err != nil {
		return err
	}
	result, err := doctor.RunDoctor(cwd, cfg, os.Getenv)
	if err != nil {
		return err
	}
	for _, line := range result.Infos {
		fmt.Fprintf(os.Stdout, "ok: %s\n", line)
	}
	for _, line := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", line)
	}
	for _, line := range result.Errors {
		fmt.Fprintf(os.Stderr, "error: %s\n", line)
	}
	if len(result.Errors) > 0 {
		return hverrors.New("Doctor found blocking errors.")
	}
	return nil
}

// migrateCommand converts a legacy hooversion.config.{ts,mjs,js,cjs} into
// hooversion.yaml. An explicit positional path wins; otherwise the legacy
// path comes from config discovery. Modern-only or empty directories are
// reported as nothing to migrate.
func migrateCommand(cwd string, flags *cliFlags) error {
	if len(flags.positionals) > 1 {
		return hverrors.New("Unexpected positional argument: %s", flags.positionals[1])
	}

	tsPath := ""
	if len(flags.positionals) == 1 {
		tsPath = filepath.Join(cwd, flags.positionals[0])
	} else {
		_, err := config.FindPath(cwd)
		var legacy *config.LegacyConfigError
		switch {
		case err == nil:
			// A modern config (or none) exists; nothing to migrate.
		case errors.As(err, &legacy):
			tsPath = legacy.Path
		default:
			return err
		}
	}

	if tsPath == "" {
		fmt.Fprintln(os.Stdout, "No legacy Hooversion config found.")
		return nil
	}

	_, yamlPath, err := config.MigrateFromTS(cwd, tsPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Wrote %s\n", yamlPath)
	return nil
}

// buildPlan loads the current branch and derives the release plan.
func buildPlan(cwd string, cfg *types.NormalizedConfig) (*types.ReleasePlan, error) {
	branch, err := git.CurrentBranch(cwd)
	if err != nil {
		return nil, err
	}
	return plan.CreatePlan(cwd, cfg, branch, nil)
}

// printPlan renders the plan layout asserted by tests and scripts.
func printPlan(p *types.ReleasePlan) {
	fmt.Fprintf(os.Stdout, "Branch: %s\n", p.Branch)
	if len(p.UnmatchedCommits) > 0 {
		fmt.Fprintln(os.Stdout, "Unmatched release commits:")
		for _, c := range p.UnmatchedCommits {
			fmt.Fprintf(os.Stdout, "- %s\n", formatCommit(c))
		}
		return
	}

	if len(p.Releases) == 0 {
		fmt.Fprintln(os.Stdout, "No release needed.")
		return
	}

	fmt.Fprintln(os.Stdout, "Planned releases:")
	for _, r := range p.Releases {
		source := "from repository history"
		if r.LatestTag != "" {
			source = "since " + r.LatestTag
		}
		dependency := ""
		if r.DependencyTriggered {
			dependency = " dependency-propagated"
		}
		fmt.Fprintf(os.Stdout, "- %s: %s -> %s (%s%s, %s) tag %s\n",
			r.Package.Name, r.CurrentVersion, r.NextVersion, r.ReleaseType, dependency, source, r.Tag)
		for _, c := range r.Commits {
			fmt.Fprintf(os.Stdout, "  - %s\n", formatCommit(c))
		}
	}
}

// formatCommit renders "<hash7> <type>(<scope>)<!>: <description>"; the
// commit package's Format deliberately excludes the hash, so it is
// prepended here.
func formatCommit(c types.ParsedCommit) string {
	return hash7(c.Hash) + " " + commit.Format(c)
}

func hash7(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

func printHelp() {
	fmt.Fprint(os.Stdout, `hooversion

Usage:
  hooversion init [--force] [--no-workflow] [--action-owner-repo <owner/repo>] [--action-ref <ref>] [--hooversion-version <version>]
  hooversion lint --last
  hooversion lint --from <ref> [--to <ref>]
  hooversion lint --edit <commit-msg-file>
  hooversion plan [--config <path>]
  hooversion release [--dry-run] [--no-push] [--no-github] [--config <path>]
  hooversion verify-release [--repository <owner/repo>] [--tag <tag>] [--checksums <asset>] [--require-sbom] [--require-license]
                            [--require-signatures --signature-identity <identity> --signature-issuer <issuer>]
                            [--require-attestations --signer-workflow <owner/repo/path>] [--source-ref <ref>]
                            [--require-signed-tag] [--output <path>]
  hooversion doctor [--config <path>]
  hooversion app
`+"\n")
}

func printVersion(version string) {
	fmt.Fprintf(os.Stdout, "hooversion %s\n", version)
}
