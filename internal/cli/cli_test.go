// Tests for the CLI surface, ported from tests/cli.test.ts plus the
// flag-strictness, doctor, migrate, help, and version behaviors of the
// contract. Config fixtures use hooversion.yaml (the Go default format).
package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/verifyrelease"
)

const nodeAppConfig = "packages:\n  - name: app\n    type: node\ngithub:\n  enabled: false\npush: false\n"

const twoPackageConfig = "packages:\n  - name: a\n    type: node\n    path: a\n  - name: b\n    type: node\n    path: b\ngithub:\n  enabled: false\npush: false\n"

// runCLI swaps stdout/stderr, chdirs into cwd, runs Run, and restores.
func runCLI(t *testing.T, cwd, version string, args ...string) (string, string, int) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutW, stderrW
	code := Run(args, version)
	os.Stdout, os.Stderr = oldOut, oldErr
	stdoutW.Close()
	stderrW.Close()
	outBuf, _ := io.ReadAll(stdoutR)
	errBuf, _ := io.ReadAll(stderrR)
	return string(outBuf), string(errBuf), code
}

func gitHelper(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	gitHelper(t, cwd, "init", "-b", "main")
	gitHelper(t, cwd, "config", "user.email", "test@example.com")
	gitHelper(t, cwd, "config", "user.name", "Hooversion Test")
	return cwd
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustContain(t *testing.T, haystack, needle, label string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%s missing %q; got:\n%s", label, needle, haystack)
	}
}

func TestUnknownOptionAndCommandFailBeforeSideEffects(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
	configPath := filepath.Join(cwd, "hooversion.yaml")
	writeFile(t, configPath, nodeAppConfig)

	_, stderr, code := runCLI(t, cwd, "dev", "release", "--unknown")
	if code == 0 {
		t.Fatal("unknown option should exit nonzero")
	}
	mustContain(t, stderr, "Unknown option for release: --unknown", "stderr")

	before, _ := os.ReadFile(filepath.Join(cwd, "package.json"))
	after, _ := os.ReadFile(filepath.Join(cwd, "package.json"))
	if string(before) != string(after) {
		t.Fatal("release side effects ran despite unknown option")
	}

	_, stderr, code = runCLI(t, cwd, "dev", "wat")
	if code == 0 {
		t.Fatal("unknown command should exit nonzero")
	}
	mustContain(t, stderr, "Unknown command: wat", "stderr")
}

func TestFlagStrictnessRejections(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing value", []string{"plan", "--config"}, "Option requires a non-empty value: --config"},
		{"dash-leading value", []string{"plan", "--config", "--other"}, "Option requires a non-empty value: --config"},
		{"duplicate option", []string{"release", "--config", "a", "--config", "b"}, "Option may only be specified once: --config"},
		{"boolean inline value", []string{"init", "--force=true"}, "Boolean option does not accept a value: --force"},
		{"positional garbage", []string{"lint", "--last", "garbage"}, "Unexpected positional argument: garbage"},
		{"empty long option", []string{"release", "--"}, "Unknown option for release: --"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			_, stderr, code := runCLI(t, cwd, "dev", tc.args...)
			if code == 0 {
				t.Fatalf("%v should exit nonzero", tc.args)
			}
			mustContain(t, stderr, tc.want, "stderr")
		})
	}
}

func TestInitRefusesOverwriteWithoutForce(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "hooversion.yaml"), nodeAppConfig)

	_, stderr, code := runCLI(t, cwd, "dev", "init")
	if code == 0 {
		t.Fatal("init without --force over existing config should fail")
	}
	mustContain(t, stderr, "Hooversion config already exists. Use --force to overwrite.", "stderr")
}

func TestInitRefusesAllModernConfigVariants(t *testing.T) {
	for _, name := range []string{".hooversion.yaml", "hooversion.yml", ".hooversion.yml", "hooversion.json"} {
		t.Run("refuse "+name, func(t *testing.T) {
			cwd := t.TempDir()
			writeFile(t, filepath.Join(cwd, name), nodeAppConfig)
			_, stderr, code := runCLI(t, cwd, "dev", "init")
			if code == 0 {
				t.Fatalf("init must refuse over existing %s", name)
			}
			mustContain(t, stderr, "Hooversion config already exists. Use --force to overwrite.", "stderr")
		})
	}

	t.Run("force replaces lone hooversion.yml", func(t *testing.T) {
		cwd := t.TempDir()
		writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
		yml := filepath.Join(cwd, "hooversion.yml")
		writeFile(t, yml, nodeAppConfig)

		stdout, _, code := runCLI(t, cwd, "dev", "init", "--no-workflow")
		if code == 0 {
			t.Fatal("--force is required to replace an existing config")
		}
		_, stderr, _ := runCLI(t, cwd, "dev", "init")
		mustContain(t, stderr, "Hooversion config already exists. Use --force to overwrite.", "stderr")

		stdout, _, code = runCLI(t, cwd, "dev", "init", "--no-workflow", "--force")
		if code != 0 {
			t.Fatalf("init --force over lone yml failed:\n%s", stdout)
		}
		if _, err := os.Stat(filepath.Join(cwd, "hooversion.yaml")); err != nil {
			t.Fatal("hooversion.yaml should be written")
		}
		if _, err := os.Stat(yml); !os.IsNotExist(err) {
			t.Fatal("the previously selected config must be removed under --force")
		}
	})
}

func TestInitDuplicateConfigsRefuseForce(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
	writeFile(t, filepath.Join(cwd, "hooversion.config.ts"), "export default {};\n")
	writeFile(t, filepath.Join(cwd, "hooversion.config.json"), "{}\n")

	_, stderr, code := runCLI(t, cwd, "dev", "init", "--force")
	if code == 0 {
		t.Fatal("--force with duplicate legacy configs should fail")
	}
	mustContain(t, stderr,
		"Multiple Hooversion configs exist; remove duplicate config files before using --force.", "stderr")
	if _, err := os.Stat(filepath.Join(cwd, "hooversion.yaml")); !os.IsNotExist(err) {
		t.Fatal("no yaml should be written on duplicate-config failure")
	}
}

func TestInitDetectFailureMessage(t *testing.T) {
	cwd := t.TempDir()

	_, stderr, code := runCLI(t, cwd, "dev", "init")
	if code == 0 {
		t.Fatal("init in empty directory should fail")
	}
	mustContain(t, stderr,
		"Could not detect package.json, Cargo.toml, pyproject.toml, or version.", "stderr")
}

func TestInitWorkflowCollisionKeepsConfigUnwritten(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
	writeFile(t, filepath.Join(cwd, ".github", "workflows", "ci.yml"), "name: User CI\n")

	_, stderr, code := runCLI(t, cwd, "dev", "init")
	if code == 0 {
		t.Fatal("init colliding with a user workflow should fail")
	}
	mustContain(t, stderr, "Refusing to overwrite existing workflow", "stderr")
	if _, err := os.Stat(filepath.Join(cwd, "hooversion.yaml")); !os.IsNotExist(err) {
		t.Fatal("config must not be written when workflow initialization collides")
	}
}

func TestInitWritesConfigAndWorkflowsAndForceReplacesLegacy(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
	legacyJSON := filepath.Join(cwd, "hooversion.config.json")
	writeFile(t, legacyJSON, `{"packages":[{"name":"app","type":"node"}]}`)

	stdout, _, code := runCLI(t, cwd, "dev", "init", "--force")
	if code != 0 {
		t.Fatalf("init --force failed: %s", stdout)
	}
	yamlPath := filepath.Join(cwd, "hooversion.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(data), "packages:", "written yaml")
	if _, err := os.Stat(legacyJSON); !os.IsNotExist(err) {
		t.Fatal("--force should remove the previously selected config")
	}
	for _, wrote := range []string{
		"Wrote " + yamlPath,
		"Wrote " + filepath.Join(cwd, ".github", "workflows", "ci.yml"),
		"Wrote " + filepath.Join(cwd, ".github", "workflows", "release.yml"),
	} {
		mustContain(t, stdout, wrote+"\n", "stdout")
	}
	ciData, err := os.ReadFile(filepath.Join(cwd, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(ciData), "# Generated by Hooversion\n") {
		t.Fatalf("ci.yml lacks generated marker: %q", ciData)
	}

	// The written config loads cleanly and rerunning init --force keeps it.
	if _, _, code := runCLI(t, cwd, "dev", "init", "--no-workflow"); code == 0 {
		t.Fatal("rerun without --force must still refuse over the fresh config")
	}
}

func TestLintSelectorsConflictsAndInvalidRefs(t *testing.T) {
	cwd := newRepo(t)
	messagePath := filepath.Join(cwd, "message.txt")
	writeFile(t, messagePath, "fix: repair\n")

	if _, stderr, code := runCLI(t, cwd, "dev", "lint", "--last", "--edit", "message.txt"); code == 0 {
		t.Fatal("conflicting selectors should fail")
	} else {
		mustContain(t, stderr,
			"lint requires exactly one selector: --last, --edit <file>, or --from <ref> [--to <ref>].", "stderr")
	}
	if _, _, code := runCLI(t, cwd, "dev", "lint"); code == 0 {
		t.Fatal("missing selectors should fail")
	}
	if _, stderr, code := runCLI(t, cwd, "dev", "lint", "--to", "HEAD"); code == 0 {
		t.Fatal("--to without --from should fail")
	} else {
		mustContain(t, stderr, "--to requires --from.", "stderr")
	}
	if _, stderr, code := runCLI(t, cwd, "dev", "lint", "--from", "does-not-exist"); code == 0 {
		t.Fatal("invalid ref should fail")
	} else {
		mustContain(t, stderr, "Invalid git ref for --from: does-not-exist", "stderr")
	}
}

func TestLintEditFlowSuccessAndFailure(t *testing.T) {
	cwd := newRepo(t)
	good := filepath.Join(cwd, "good.txt")
	writeFile(t, good, "fix: repair\r\nbody line\r\n")

	stdout, _, code := runCLI(t, cwd, "dev", "lint", "--edit", "good.txt")
	if code != 0 {
		t.Fatalf("valid edit lint failed:\n%s", stdout)
	}
	mustContain(t, stdout, "Validated 1 commit.\n", "stdout")

	bad := filepath.Join(cwd, "bad.txt")
	writeFile(t, bad, "Bad Subject no colon\n")
	_, stderr, code := runCLI(t, cwd, "dev", "lint", "--edit", "bad.txt")
	if code != 1 {
		t.Fatalf("invalid edit lint should exit 1, got %d", code)
	}
	mustContain(t, stderr, "Bad Subject no colon\n  header must match", "stderr")
	mustContain(t, stderr, "Commit lint failed with 1 issue(s).", "stderr")
}

func TestLintLastAndRangePluralization(t *testing.T) {
	cwd := newRepo(t)
	writeFile(t, filepath.Join(cwd, "a.txt"), "a\n")
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "fix: first")
	writeFile(t, filepath.Join(cwd, "b.txt"), "b\n")
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "feat: second")

	stdout, _, code := runCLI(t, cwd, "dev", "lint", "--last")
	if code != 0 {
		t.Fatalf("lint --last failed:\n%s", stdout)
	}
	mustContain(t, stdout, "Validated 1 commit.\n", "stdout")

	writeFile(t, filepath.Join(cwd, "c.txt"), "c\n")
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "fix: third")

	stdout, _, code = runCLI(t, cwd, "dev", "lint", "--from", "HEAD~2")
	if code != 0 {
		t.Fatalf("lint --from failed:\n%s", stdout)
	}
	mustContain(t, stdout, "Validated 2 commits.\n", "stdout")
}
func TestPlanNoReleaseNeeded(t *testing.T) {
	cwd := newRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
	writeFile(t, filepath.Join(cwd, "hooversion.yaml"), nodeAppConfig)
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "chore: initial import")

	stdout, _, code := runCLI(t, cwd, "dev", "plan")
	if code != 0 {
		t.Fatalf("plain plan should succeed:\n%s", stdout)
	}
	mustContain(t, stdout, "Branch: main\n", "stdout")
	mustContain(t, stdout, "No release needed.\n", "stdout")
}

func TestPlanUnmatchedCommitsAreNonzero(t *testing.T) {
	cwd := newRepo(t)
	writeFile(t, filepath.Join(cwd, "a", "package.json"), `{"name":"a","version":"1.0.0"}`)
	writeFile(t, filepath.Join(cwd, "b", "package.json"), `{"name":"b","version":"1.0.0"}`)
	writeFile(t, filepath.Join(cwd, "hooversion.yaml"), twoPackageConfig)
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "chore: initial import")
	gitHelper(t, cwd, "tag", "a@v1.0.0")
	gitHelper(t, cwd, "tag", "b@v1.0.0")
	writeFile(t, filepath.Join(cwd, "README.txt"), "root change\n")
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "feat: change outside packages")

	stdout, _, code := runCLI(t, cwd, "dev", "plan")
	if code == 0 {
		t.Fatal("unmatched plan must exit nonzero")
	}
	mustContain(t, stdout, "Unmatched release commits:\n", "stdout")
}

func TestReleaseDryRunPrintsPlanAndCompletionLine(t *testing.T) {
	cwd := newRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
	writeFile(t, filepath.Join(cwd, "hooversion.yaml"), nodeAppConfig)
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "chore: initial import")
	gitHelper(t, cwd, "tag", "v1.0.0")
	writeFile(t, filepath.Join(cwd, "app.ts"), "export const app = true;\n")
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "fix: repair app")

	stdout, _, code := runCLI(t, cwd, "dev", "release", "--dry-run", "--no-push", "--no-github")
	if code != 0 {
		t.Fatalf("dry-run release failed:\n%s", stdout)
	}
	mustContain(t, stdout, "Planned releases:", "stdout")
	mustContain(t, stdout, "since v1.0.0", "stdout")
	mustContain(t, stdout, "Dry run complete; no files, commits, tags, or releases were created.\n", "stdout")
}

func TestReleaseResumedRunReportsComplete(t *testing.T) {
	cwd := newRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
	writeFile(t, filepath.Join(cwd, "hooversion.yaml"), nodeAppConfig)
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "chore: initial import")
	gitHelper(t, cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	writeFile(t, filepath.Join(cwd, "app.ts"), "export const app = true;\n")
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "fix: repair app")

	stdout, _, code := runCLI(t, cwd, "dev", "release", "--no-push", "--no-github")
	if code != 0 {
		t.Fatalf("first release failed:\n%s", stdout)
	}
	resumedStdout, _, code := runCLI(t, cwd, "dev", "release", "--no-push", "--no-github")
	if code != 0 {
		t.Fatalf("resumed release failed:\n%s", resumedStdout)
	}
	mustContain(t, resumedStdout, "Release complete.", "stdout")
	if strings.Contains(resumedStdout, "No release needed.") {
		t.Fatal("resumed release must not report No release needed.")
	}
}

func TestDoctorPrefixRenderingAndBlockingErrors(t *testing.T) {
	// Blocking: valid config outside any git repository.
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0"}`)
	writeFile(t, filepath.Join(cwd, "hooversion.yaml"), nodeAppConfig)

	_, stderr, code := runCLI(t, cwd, "dev", "doctor")
	if code == 0 {
		t.Fatal("doctor outside a git repository must fail")
	}
	mustContain(t, stderr, "error: Current directory is not a git repository.\n", "stderr")
	mustContain(t, stderr, "Doctor found blocking errors.", "stderr")

	// Healthy repo: ok lines on stdout, tag warning on stderr.
	repo := newRepo(t)
	writeFile(t, filepath.Join(repo, "package.json"), `{"name":"app","version":"1.0.0"}`)
	writeFile(t, filepath.Join(repo, "hooversion.yaml"), nodeAppConfig)
	gitHelper(t, repo, "add", "--all")
	gitHelper(t, repo, "commit", "-m", "chore: initial import")

	stdout, stderr, code := runCLI(t, repo, "dev", "doctor")
	if code != 0 {
		t.Fatalf("healthy doctor should pass:\n%s\n%s", stdout, stderr)
	}
	mustContain(t, stdout, "ok: Release branch: main\n", "stdout")
	mustContain(t, stdout, "ok: app: manifest version 1.0.0\n", "stdout")
	mustContain(t, stderr, "warning: app: no release tag found; first release will use full reachable history.\n", "stderr")
}

func TestMigrateBranches(t *testing.T) {
	// Nothing to migrate at all.
	empty := t.TempDir()
	stdout, _, code := runCLI(t, empty, "dev", "migrate")
	if code != 0 {
		t.Fatalf("migrate with no config should succeed:\n%s", stdout)
	}
	mustContain(t, stdout, "No legacy Hooversion config found.", "stdout")

	// Modern-only configuration also reports nothing to migrate.
	modern := t.TempDir()
	writeFile(t, filepath.Join(modern, "hooversion.yaml"), nodeAppConfig)
	stdout, _, code = runCLI(t, modern, "dev", "migrate")
	if code != 0 {
		t.Fatalf("migrate with modern config should succeed:\n%s", stdout)
	}
	mustContain(t, stdout, "No legacy Hooversion config found.", "stdout")

	// Legacy config present without bun on PATH: verbatim bun-missing error.
	legacy := t.TempDir()
	tsPath := filepath.Join(legacy, "hooversion.config.ts")
	writeFile(t, tsPath, "export default { packages: [{ name: 'app', path: '.', type: 'node' }] };\n")
	t.Setenv("PATH", "")
	_, stderr, code := runCLI(t, legacy, "dev", "migrate")
	if code == 0 {
		t.Fatal("migrate without bun must fail")
	}
	mustContain(t, stderr, tsPath+" is a legacy Hooversion config; migrating it requires bun.", "stderr")

	// Non-migrate commands surface the typed discovery error verbatim.
	hint := t.TempDir()
	writeFile(t, filepath.Join(hint, "hooversion.config.ts"), "export default {};\n")
	_, stderr, _ = runCLI(t, hint, "dev", "doctor")
	mustContain(t, stderr, "run `hooversion migrate` to convert it to hooversion.yaml.", "stderr")

	// Extra positionals are rejected with the standard message.
	extra := t.TempDir()
	_, stderr, _ = runCLI(t, extra, "dev", "migrate", "one.ts", "two.ts")
	mustContain(t, stderr, "Unexpected positional argument: two.ts", "stderr")
}

func TestVersionHelpAndDefaultCommand(t *testing.T) {
	cwd := t.TempDir()

	const wantHelp = "hooversion\n" +
		"\n" +
		"Usage:\n" +
		"  hooversion init [--force] [--no-workflow] [--action-owner-repo <owner/repo>] [--action-ref <ref>] [--hooversion-version <version>]\n" +
		"  hooversion lint --last\n" +
		"  hooversion lint --from <ref> [--to <ref>]\n" +
		"  hooversion lint --edit <commit-msg-file>\n" +
		"  hooversion plan [--config <path>]\n" +
		"  hooversion release [--dry-run] [--no-push] [--no-github] [--config <path>]\n" +
		"  hooversion verify-release [--repository <owner/repo>] [--tag <tag>] [--checksums <asset>] [--require-sbom] [--require-license]\n" +
		"                            [--require-signatures --signature-identity <identity> --signature-issuer <issuer>]\n" +
		"                            [--require-attestations --signer-workflow <owner/repo/path>] [--source-ref <ref>]\n" +
		"                            [--require-signed-tag] [--output <path>]\n" +
		"  hooversion doctor [--config <path>]\n" +
		"  hooversion app\n\n"

	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {}} {
		stdout, _, code := runCLI(t, cwd, "dev", args...)
		if code != 0 || stdout != wantHelp {
			t.Fatalf("help %v mismatch (code %d):\n%q", args, code, stdout)
		}
	}

	stdout, _, code := runCLI(t, cwd, "9.9.9-test", "version")
	if code != 0 || stdout != "hooversion 9.9.9-test\n" {
		t.Fatalf("version output = %q (code %d)", stdout, code)
	}
	stdout, _, _ = runCLI(t, cwd, "dev", "--version")
	if stdout != "hooversion dev\n" {
		t.Fatalf("--version output = %q", stdout)
	}
	stdout, _, _ = runCLI(t, cwd, "dev", "-v")
	if stdout != "hooversion dev\n" {
		t.Fatalf("-v output = %q", stdout)
	}
}

func TestVerifyReleaseMapsStrictPolicyAndWritesVSAExclusively(t *testing.T) {
	cwd := t.TempDir()
	output := filepath.Join(cwd, "release.vsa.json")
	saved := runVerifyRelease
	defer func() { runVerifyRelease = saved }()
	var received verifyrelease.Options
	runVerifyRelease = func(_ context.Context, options verifyrelease.Options) (verifyrelease.Result, error) {
		received = options
		return verifyrelease.Result{
			Repository: options.Repository,
			Tag:        options.Tag,
			TagCommit:  strings.Repeat("a", 40),
			Statement: verifyrelease.Statement{
				Type:          "https://in-toto.io/Statement/v1",
				PredicateType: verifyrelease.VSAPredicateType,
				Subject: []verifyrelease.Subject{
					{Name: "artifact.tar.gz", Digest: map[string]string{"sha256": strings.Repeat("b", 64)}},
					{Name: "SHA256SUMS", Digest: map[string]string{"sha256": strings.Repeat("c", 64)}},
				},
			},
		}, nil
	}
	t.Setenv("GH_TOKEN", "test-token")
	stdout, stderr, code := runCLI(t, cwd, "dev",
		"verify-release", "--repository", "openhoo/demo", "--tag", "v1.2.3",
		"--checksums", "SUMS", "--require-sbom", "--require-license", "--require-signed-tag",
		"--require-signatures", "--signature-identity", "identity", "--signature-issuer", "issuer",
		"--require-attestations", "--signer-workflow", "openhoo/demo/.github/workflows/release.yml",
		"--source-ref", "refs/heads/main", "--verifier-id", "verifier", "--policy-uri", "policy",
		"--api-url", "https://api.example.test", "--output", output,
	)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if received.Repository != "openhoo/demo" || received.Token != "test-token" || received.ChecksumsAsset != "SUMS" ||
		!received.RequireSBOM || !received.RequireLicense || !received.RequireSignedTag || !received.RequireSignatures ||
		!received.RequireAttestations || received.SignatureIdentity != "identity" || received.SignatureIssuer != "issuer" ||
		received.SignerWorkflow != "openhoo/demo/.github/workflows/release.yml" || received.SourceRef != "refs/heads/main" ||
		received.VerifierID != "verifier" || received.PolicyURI != "policy" || received.APIURL != "https://api.example.test" {
		t.Fatalf("options not mapped: %#v", received)
	}
	if !strings.Contains(stdout, "Verified 2 release artifacts for openhoo/demo@v1.2.3") {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
	data, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(data), verifyrelease.VSAPredicateType) {
		t.Fatalf("VSA data=%q err=%v", data, err)
	}
	if _, _, code := runCLI(t, cwd, "dev", "verify-release", "--repository", "openhoo/demo", "--tag", "v1.2.3", "--output", output); code == 0 {
		t.Fatal("existing VSA output was overwritten")
	}
}

func TestAppNilGuard(t *testing.T) {
	cwd := t.TempDir()
	saved := AppEntry
	AppEntry = nil
	defer func() { AppEntry = saved }()

	_, stderr, code := runCLI(t, cwd, "dev", "app")
	if code == 0 {
		t.Fatal("app with nil AppEntry must fail")
	}
	mustContain(t, stderr, "app command requires the versionhoo-app binary", "stderr")
}

func TestExitErrorPropagatesCodeOneForPlainErrors(t *testing.T) {
	cwd := t.TempDir()
	// A broken explicit config path yields a plain load failure printed as-is.
	_, stderr, code := runCLI(t, cwd, "dev", "plan", "--config", "missing.yaml")
	if code != 1 {
		t.Fatalf("plain errors must exit 1, got %d", code)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("plain errors must print a message")
	}
}

// Guard against accidental format drift in the hash7 helper.
func TestHash7ShortInput(t *testing.T) {
	if got := hash7("abc"); got != "abc" {
		t.Fatalf("hash7 short input = %q", got)
	}
	if got := hash7("1234567890"); got != "1234567" {
		t.Fatalf("hash7 long input = %q", got)
	}
}
