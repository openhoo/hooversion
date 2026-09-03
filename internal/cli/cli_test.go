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

	"github.com/openhoo/hooversion/internal/types"
	"github.com/openhoo/hooversion/internal/verifyrelease"
	"gopkg.in/yaml.v3"
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

func parseWorkflowYAML(t *testing.T, data []byte) *yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("workflow YAML is invalid: %v\n%s", err, data)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("workflow YAML root = %#v, want one mapping document", document)
	}
	return document.Content[0]
}

func workflowMapValue(t *testing.T, mapping *yaml.Node, key string) *yaml.Node {
	t.Helper()
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		t.Fatalf("YAML node for %q is not a mapping: %#v", key, mapping)
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	t.Fatalf("YAML mapping missing key %q", key)
	return nil
}

func workflowOptionalMapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func workflowStep(t *testing.T, steps *yaml.Node, name string) *yaml.Node {
	t.Helper()
	if steps.Kind != yaml.SequenceNode {
		t.Fatalf("steps node = %#v, want sequence", steps)
	}
	for _, step := range steps.Content {
		if workflowOptionalMapValue(step, "name") != nil &&
			workflowOptionalMapValue(step, "name").Value == name {
			return step
		}
	}
	t.Fatalf("steps missing %q", name)
	return nil
}

func assertWorkflowMapKeys(t *testing.T, mapping *yaml.Node, want ...string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, key := range want {
		wantSet[key] = true
	}
	if mapping.Kind != yaml.MappingNode {
		t.Fatalf("node = %#v, want mapping", mapping)
	}
	var got []string
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		got = append(got, mapping.Content[i].Value)
		if !wantSet[mapping.Content[i].Value] {
			t.Fatalf("unexpected YAML key %q in %#v", mapping.Content[i].Value, mapping)
		}
	}
	if len(got) != len(wantSet) {
		t.Fatalf("YAML keys = %v, want exactly %v", got, want)
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
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0","packageManager":"bun@1.3.14","scripts":{"check":"echo ok"}}`)
	writeFile(t, filepath.Join(cwd, "bun.lock"), "lockfileVersion: 1\n")
	writeFile(t, filepath.Join(cwd, ".github", "workflows", "ci.yml"), "name: User CI\n")

	_, stderr, code := runCLI(t, cwd, "dev", "init", "--hooversion-version", "1.1.0")
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
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","version":"1.0.0","packageManager":"bun@1.3.14","scripts":{"check":"echo ok"}}`)
	writeFile(t, filepath.Join(cwd, "bun.lock"), "lockfileVersion: 1\n")
	legacyJSON := filepath.Join(cwd, "hooversion.config.json")
	writeFile(t, legacyJSON, `{"packages":[{"name":"app","type":"node"}]}`)

	stdout, _, code := runCLI(t, cwd, "dev", "init", "--force", "--hooversion-version", "1.1.0")
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

func TestWorkflowInferenceSelectsOnlySupportedCommands(t *testing.T) {
	t.Run("bun", func(t *testing.T) {
		cwd := t.TempDir()
		writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","packageManager":"bun@1.3.14","scripts":{"check":"echo ok"}}`)
		writeFile(t, filepath.Join(cwd, "bun.lock"), "lockfileVersion: 1\n")
		project, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}})
		if err != nil || project.kind != workflowBun || project.bunVersion != "1.3.14" || project.installCommand != "bun install --frozen-lockfile" {
			t.Fatalf("project = %+v, err = %v", project, err)
		}
		rendered, err := renderGitHubWorkflows(workflowOptions{hooversionVersion: "1.1.0", actionRef: knownActionRefs["1.1.0"], project: project})
		lintStart := strings.Index(rendered.ci, "actions/lint")
		buildStart := strings.Index(rendered.ci, "  build:")
		if err != nil || lintStart < 0 || buildStart < 0 || strings.Contains(rendered.ci[lintStart:buildStart], "bun-version") {
			t.Fatalf("Bun workflow unexpectedly passes bun-version to lint: err=%v", err)
		}
		if !strings.Contains(rendered.release, "install-command: bun install --frozen-lockfile") {
			t.Fatal("Bun release workflow missing install command")
		}
	})

	t.Run("bun test script", func(t *testing.T) {
		cwd := t.TempDir()
		writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","packageManager":"bun@1.4.0","scripts":{"test":"bun run vitest --run"}}`)
		project, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}})
		if err != nil || project.checkCommand != "bun run test" {
			t.Fatalf("project = %+v, err = %v", project, err)
		}
	})

	t.Run("npm engine", func(t *testing.T) {
		cwd := t.TempDir()
		writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","packageManager":"npm@10.9.2","engines":{"node":">=20 <22"},"scripts":{"test":"npm run vitest"}}`)
		writeFile(t, filepath.Join(cwd, "package-lock.json"), "{}\n")
		project, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}})
		if err != nil || project.kind != workflowNode || project.nodeVersion != "20" || project.checkCommand != "npm run test" {
			t.Fatalf("project = %+v, err = %v", project, err)
		}
		rendered, err := renderGitHubWorkflows(workflowOptions{hooversionVersion: "1.1.0", actionRef: knownActionRefs["1.1.0"], project: project})
		if err != nil || !strings.Contains(rendered.ci, "node-version: 20") {
			t.Fatalf("Node workflow = %q, err=%v", rendered.ci, err)
		}
	})

	t.Run("npm version file", func(t *testing.T) {
		cwd := t.TempDir()
		writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","packageManager":"npm@10.9.2"}`)
		writeFile(t, filepath.Join(cwd, "package-lock.json"), "{}\n")
		writeFile(t, filepath.Join(cwd, ".nvmrc"), "v22.11.0\n")
		project, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}})
		if err != nil || project.nodeVersion != "22.11.0" {
			t.Fatalf("project = %+v, err = %v", project, err)
		}
	})

	t.Run("npm missing runtime evidence", func(t *testing.T) {
		cwd := t.TempDir()
		writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","packageManager":"npm@10.9.2"}`)
		writeFile(t, filepath.Join(cwd, "package-lock.json"), "{}\n")
		if _, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}}); err == nil || !strings.Contains(err.Error(), "--no-workflow") {
			t.Fatalf("error = %v, want manual workflow guidance", err)
		}
	})

	t.Run("rust", func(t *testing.T) {
		cwd := t.TempDir()
		writeFile(t, filepath.Join(cwd, "Cargo.lock"), "version = 4\n")
		project, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageRust}})
		if err != nil || project.checkCommand != "cargo test --locked --workspace --all-targets --all-features" {
			t.Fatalf("project = %+v, err = %v", project, err)
		}
		rendered, err := renderGitHubWorkflows(workflowOptions{hooversionVersion: "1.1.0", actionRef: knownActionRefs["1.1.0"], project: project})
		if err != nil || strings.Contains(rendered.ci, "setup-bun") || !strings.Contains(rendered.ci, project.checkCommand) {
			t.Fatalf("Rust workflow = %q, err=%v", rendered.ci, err)
		}
	})

	t.Run("version-file", func(t *testing.T) {
		cwd := t.TempDir()
		project, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageVersionFile}})
		if err != nil || project.installCommand != "" || project.checkCommand != "" {
			t.Fatalf("project = %+v, err = %v", project, err)
		}
	})
}

func TestWorkflowInferenceRefusesAmbiguousNodeRuntimeEvidence(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","packageManager":"npm@10.9.2","engines":{"node":"20"}}`)
	writeFile(t, filepath.Join(cwd, "package-lock.json"), "{}\n")
	writeFile(t, filepath.Join(cwd, ".node-version"), "22\n")
	if _, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}}); err == nil || !strings.Contains(err.Error(), "--no-workflow") {
		t.Fatalf("error = %v, want manual workflow guidance", err)
	}
}

func TestWorkflowInferenceRefusesInvalidNodeRuntimeRanges(t *testing.T) {
	for _, engine := range []string{">=22 <20", ">=20 garbage"} {
		t.Run(engine, func(t *testing.T) {
			cwd := t.TempDir()
			writeFile(t, filepath.Join(cwd, "package.json"), `{"name":"app","packageManager":"npm@10.9.2","engines":{"node":"`+engine+`"}}`)
			writeFile(t, filepath.Join(cwd, "package-lock.json"), "{}\n")
			if _, err := inferWorkflowProject(cwd, []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}}); err == nil || !strings.Contains(err.Error(), "--no-workflow") {
				t.Fatalf("engine %q error = %v, want manual workflow guidance", engine, err)
			}
		})
	}
}

func TestWorkflowInferenceRefusesPythonAndMixedLayouts(t *testing.T) {
	for name, packages := range map[string][]types.PackageConfig{
		"python": {{Name: "app", Path: ".", Type: types.PackagePython}},
		"mixed":  {{Name: "app", Path: ".", Type: types.PackageNode}, {Name: "lib", Path: ".", Type: types.PackageVersionFile}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := inferWorkflowProject(t.TempDir(), packages)
			if err == nil || !strings.Contains(err.Error(), "--no-workflow") {
				t.Fatalf("error = %v, want manual workflow guidance", err)
			}
		})
	}
}

func TestGeneratedReleaseWorkflowGuardsDispatchAndWorkflowRun(t *testing.T) {
	rendered, err := renderGitHubWorkflows(workflowOptions{
		hooversionVersion: "1.1.0",
		project:           workflowProject{kind: workflowVersionFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	ciRoot := parseWorkflowYAML(t, []byte(rendered.ci))
	releaseRoot := parseWorkflowYAML(t, []byte(rendered.release))
	if workflowOptionalMapValue(workflowMapValue(t, ciRoot, "on"), "workflow_dispatch") == nil {
		t.Fatal("CI workflow must retain its manual dispatch trigger")
	}
	if workflowOptionalMapValue(workflowMapValue(t, releaseRoot, "on"), "workflow_dispatch") == nil {
		t.Fatal("release workflow must retain its manual dispatch trigger")
	}
	releaseJob := workflowMapValue(t, workflowMapValue(t, releaseRoot, "jobs"), "release")
	guard := workflowMapValue(t, releaseJob, "if").Value
	dispatchRuns := func(eventName, ref string) bool {
		const dispatchPrefix = "github.event_name == 'workflow_dispatch' && github.ref == '"
		start := strings.Index(guard, dispatchPrefix)
		if start < 0 {
			return false
		}
		remainder := guard[start+len(dispatchPrefix):]
		end := strings.IndexByte(remainder, '\'')
		return end > 0 && eventName == "workflow_dispatch" && ref == remainder[:end]
	}
	for _, tc := range []struct {
		name      string
		eventName string
		ref       string
		want      bool
	}{
		{name: "main dispatch", eventName: "workflow_dispatch", ref: "refs/heads/main", want: true},
		{name: "feature dispatch", eventName: "workflow_dispatch", ref: "refs/heads/feature", want: false},
		{name: "tag dispatch", eventName: "workflow_dispatch", ref: "refs/tags/v1.1.0", want: false},
		{name: "workflow run", eventName: "workflow_run", ref: "refs/heads/main", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchRuns(tc.eventName, tc.ref); got != tc.want {
				t.Fatalf("%s ref %q runs = %v, want %v; guard = %q", tc.eventName, tc.ref, got, tc.want, guard)
			}
		})
	}
	for _, clause := range []string{
		"github.event_name == 'workflow_run'",
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.event == 'push'",
		"github.event.workflow_run.head_branch == 'main'",
		"github.event.workflow_run.repository.full_name == github.repository",
	} {
		if !strings.Contains(guard, clause) {
			t.Fatalf("release guard = %q, missing %q", guard, clause)
		}
	}
	steps := workflowMapValue(t, releaseJob, "steps")
	prepareIf := workflowMapValue(t, workflowStep(t, steps, "Prepare protected-branch release"), "if").Value
	if !strings.Contains(prepareIf, "github.event_name == 'workflow_dispatch'") {
		t.Fatalf("prepare guard = %q, want manual dispatch path", prepareIf)
	}
	finalizeIf := workflowMapValue(t, workflowStep(t, steps, "Finalize protected-branch release"), "if").Value
	if strings.Contains(finalizeIf, "workflow_dispatch") {
		t.Fatalf("finalize guard = %q, manual dispatch must not publish", finalizeIf)
	}
	if strings.Contains(rendered.ci, "actions/lint@v") {
		t.Fatalf("mutable action ref in generated CI:\n%s", rendered.ci)
	}
	if got, err := resolveActionReference("", defaultActionOwnerRepo, "1.1.0", "v1.1.0"); err == nil || got != "" {
		t.Fatalf("tag action ref accepted: %q, %v", got, err)
	}
}

func TestGeneratedWorkflowSafelySerializesWorkingDirectories(t *testing.T) {
	for _, packagePath := range []string{
		"packages/app # prod",
		"packages/app: prod",
		`packages/"app`,
		"packages/app\ncontinue-on-error: true",
	} {
		t.Run(packagePath, func(t *testing.T) {
			cwd := t.TempDir()
			writeFile(t, filepath.Join(cwd, packagePath, "package.json"), `{"name":"app","packageManager":"bun@1.3.14","scripts":{"check":"echo ok"}}`)
			writeFile(t, filepath.Join(cwd, packagePath, "bun.lock"), "lockfileVersion: 1\n")
			paths, err := writeGitHubWorkflows(cwd, workflowOptions{
				hooversionVersion: "1.1.0",
				actionRef:         knownActionRefs[defaultActionVersion],
				packages: []types.PackageConfig{{
					Name: "app", Path: packagePath, Type: types.PackageNode,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			ciData, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			releaseData, err := os.ReadFile(paths[1])
			if err != nil {
				t.Fatal(err)
			}
			ciRoot := parseWorkflowYAML(t, ciData)
			releaseRoot := parseWorkflowYAML(t, releaseData)
			build := workflowMapValue(t, workflowMapValue(t, ciRoot, "jobs"), "build")
			steps := workflowMapValue(t, build, "steps")
			install := workflowStep(t, steps, "Install dependencies")
			check := workflowStep(t, steps, "Run checks")
			assertWorkflowMapKeys(t, install, "name", "working-directory", "run")
			assertWorkflowMapKeys(t, check, "name", "working-directory", "run")
			if got := workflowMapValue(t, install, "working-directory").Value; got != packagePath {
				t.Fatalf("CI install working-directory = %q, want %q", got, packagePath)
			}
			if got := workflowMapValue(t, check, "working-directory").Value; got != packagePath {
				t.Fatalf("CI check working-directory = %q, want %q", got, packagePath)
			}

			project, err := inferNodeWorkflowProject(cwd, types.PackageConfig{
				Name: "app", Path: packagePath, Type: types.PackageNode,
			})
			if err != nil {
				t.Fatal(err)
			}
			releaseJob := workflowMapValue(t, workflowMapValue(t, releaseRoot, "jobs"), "release")
			releaseSteps := workflowMapValue(t, releaseJob, "steps")
			for _, actionName := range []string{"Prepare protected-branch release", "Finalize protected-branch release"} {
				action := workflowStep(t, releaseSteps, actionName)
				if workflowOptionalMapValue(action, "working-directory") != nil {
					t.Fatalf("%s must execute from repository root", actionName)
				}
				with := workflowMapValue(t, action, "with")
				if got := workflowMapValue(t, with, "install-command").Value; got != releaseInstallCommand(project) {
					t.Fatalf("%s install-command = %q, want %q", actionName, got, releaseInstallCommand(project))
				}
			}
		})
	}
}

func TestNestedReleaseActionsExecuteFromRepositoryRoot(t *testing.T) {
	cwd := newRepo(t)
	packagePath := filepath.Join(cwd, "packages", "app")
	writeFile(t, filepath.Join(packagePath, "package.json"), `{"name":"app","version":"1.0.0","packageManager":"npm@10.9.2","engines":{"node":">=20 <22"},"scripts":{"check":"npm run check"}}`)
	writeFile(t, filepath.Join(packagePath, "package-lock.json"), "{}\n")

	_, stderr, code := runCLI(t, cwd, "dev", "init", "--hooversion-version", "1.1.0")
	if code != 0 {
		t.Fatalf("nested Node init failed: %s", stderr)
	}
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "chore: initial import")
	gitHelper(t, cwd, "tag", "v1.0.0")
	writeFile(t, filepath.Join(packagePath, "src", "index.js"), "export const app = true;\n")
	gitHelper(t, cwd, "add", "--all")
	gitHelper(t, cwd, "commit", "-m", "fix: repair app")

	stdout, stderr, code := runCLI(t, cwd, "dev", "release", "--dry-run", "--no-push", "--no-github")
	if code != 0 {
		t.Fatalf("root release action equivalent failed to load nested config: %s\n%s", stdout, stderr)
	}
	mustContain(t, stdout, "Dry run complete; no files, commits, tags, or releases were created.\n", "stdout")
}

func TestWorkflowActionVersionIsIndependentFromCLIVersion(t *testing.T) {
	rendered, err := renderGitHubWorkflows(workflowOptions{
		hooversionVersion: "1.2.0",
		cliVersion:        "1.2.0",
		project:           workflowProject{kind: workflowVersionFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.ci, "HOOVERSION_VERSION: 1.2.0") ||
		!strings.Contains(rendered.ci, "actions/lint@"+knownActionRefs[defaultActionVersion]+" # v"+defaultActionVersion) ||
		strings.Contains(rendered.ci, "actions/lint@v1.2.0") {
		t.Fatalf("CLI version must not select an unpublished action ref:\n%s", rendered.ci)
	}
	if !strings.Contains(rendered.release, "actions/release@"+knownActionRefs[defaultActionVersion]+" # v"+defaultActionVersion) {
		t.Fatalf("release workflow did not use default published action ref:\n%s", rendered.release)
	}
}

func TestNodePackageWorkingDirectoryReachesBuildAndReleaseSteps(t *testing.T) {
	cwd := t.TempDir()
	packagePath := filepath.Join(cwd, "packages", "app")
	writeFile(t, filepath.Join(packagePath, "package.json"), `{"name":"app","packageManager":"bun@1.3.14","scripts":{"check":"echo ok"}}`)
	writeFile(t, filepath.Join(packagePath, "bun.lock"), "lockfileVersion: 1\n")
	project, err := inferNodeWorkflowProject(cwd, types.PackageConfig{
		Name: "app", Path: "packages/app", Type: types.PackageNode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if project.workingDirectory != "packages/app" {
		t.Fatalf("working directory = %q, want packages/app", project.workingDirectory)
	}
	rendered, err := renderGitHubWorkflows(workflowOptions{
		hooversionVersion: "1.1.0",
		project:           project,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.ci, "working-directory: \"packages/app\"\n        run: bun install --frozen-lockfile") ||
		!strings.Contains(rendered.ci, "working-directory: \"packages/app\"\n        run: bun run check") {
		t.Fatalf("generated CI workflow missing package working directory:\n%s", rendered.ci)
	}
	if strings.Contains(rendered.release, "working-directory: packages/app") {
		t.Fatalf("release actions must run from repository root:\n%s", rendered.release)
	}
	if !strings.Contains(rendered.release, "install-command: \"cd -- 'packages/app' \\u0026\\u0026 bun install --frozen-lockfile\"") {
		t.Fatalf("release install command is not package-scoped:\n%s", rendered.release)
	}
}

func TestInitDetectsNestedNodeAndKeepsReleaseRuntimeParity(t *testing.T) {
	cwd := t.TempDir()
	packagePath := filepath.Join(cwd, "packages", "app")
	writeFile(t, filepath.Join(packagePath, "package.json"), `{"name":"app","version":"1.0.0","packageManager":"npm@10.9.2","engines":{"node":">=20 <22"},"scripts":{"check":"npm run check"}}`)
	writeFile(t, filepath.Join(packagePath, "package-lock.json"), "{}\n")

	_, stderr, code := runCLI(t, cwd, "dev", "init", "--hooversion-version", "1.1.0")
	if code != 0 {
		t.Fatalf("nested Node init failed: %s", stderr)
	}
	ciData, err := os.ReadFile(filepath.Join(cwd, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	releaseData, err := os.ReadFile(filepath.Join(cwd, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	setup := "uses: actions/setup-node@" + setupNodeSha + " # v7\n        with:\n          node-version: 20"
	ci := string(ciData)
	release := string(releaseData)
	if strings.Count(ci, setup) != 1 || strings.Count(release, setup) != 1 {
		t.Fatalf("Node runtime setup parity missing:\nCI:\n%s\nRelease:\n%s", ci, release)
	}
	if !strings.Contains(release, "install-command: \"cd -- 'packages/app' \\u0026\\u0026 npm ci\"") {
		t.Fatalf("release Node install command is not package-scoped:\n%s", release)
	}
	if !strings.Contains(ci, "working-directory: \"packages/app\"\n        run: npm ci") ||
		strings.Contains(release, "working-directory: packages/app") {
		t.Fatalf("nested package working directory semantics missing:\nCI:\n%s\nRelease:\n%s", ci, release)
	}
	configData, err := os.ReadFile(filepath.Join(cwd, "hooversion.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configData), `path: "packages/app"`) {
		t.Fatalf("generated config lost nested package path:\n%s", configData)
	}
}

func TestWorkflowGenerationRejectsUnsupportedMultiPackageActionContract(t *testing.T) {
	cwd := t.TempDir()
	_, err := writeGitHubWorkflows(cwd, workflowOptions{
		hooversionVersion: "1.1.0",
		actionRef:         knownActionRefs[defaultActionVersion],
		packages: []types.PackageConfig{
			{Name: "one", Path: "one", Type: types.PackageNode},
			{Name: "two", Path: "two", Type: types.PackageRust},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "multi-package") || !strings.Contains(err.Error(), "releases-json") {
		t.Fatalf("error = %v, want explicit releases-json multi-package rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(cwd, ".github", "workflows")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected generation must not create workflow directory, stat error = %v", statErr)
	}
}

func TestUnknownActionVersionCannotUseConsumerOriginTag(t *testing.T) {
	cwd := newRepo(t)
	gitHelper(t, cwd, "commit", "--allow-empty", "-m", "test: seed")
	bare := filepath.Join(t.TempDir(), "consumer.git")
	gitHelper(t, filepath.Dir(bare), "init", "--bare", bare)
	gitHelper(t, cwd, "remote", "add", "origin", bare)
	gitHelper(t, cwd, "tag", "v9.9.9")
	gitHelper(t, cwd, "push", "origin", "main", "--tags")

	if got, err := resolveActionReference(cwd, defaultActionOwnerRepo, "9.9.9", ""); err == nil || got != "" || !strings.Contains(err.Error(), "--action-ref") {
		t.Fatalf("consumer-origin tag must not resolve: %q, %v", got, err)
	}
}

func TestWorkflowVersionRequiresExplicitReleaseVersionWhenUnavailable(t *testing.T) {
	if _, err := renderGitHubWorkflows(workflowOptions{project: workflowProject{kind: workflowVersionFile}}); err == nil ||
		!strings.Contains(err.Error(), "--hooversion-version") {
		t.Fatalf("missing version error = %v", err)
	}
	if got, err := resolveWorkflowVersion("v1.2.3", "dev"); err != nil || got != "1.2.3" {
		t.Fatalf("explicit version = %q, %v", got, err)
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
