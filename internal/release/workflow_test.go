package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	On struct {
		WorkflowRun struct {
			Workflows []string `yaml:"workflows"`
			Branches  []string `yaml:"branches"`
			Types     []string `yaml:"types"`
		} `yaml:"workflow_run"`
		WorkflowDispatch struct {
			Inputs struct {
				Tag struct {
					Required bool   `yaml:"required"`
					Default  string `yaml:"default"`
				} `yaml:"tag"`
			} `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs struct {
		Prepare struct {
			If    string        `yaml:"if"`
			Steps []releaseStep `yaml:"steps"`
		} `yaml:"prepare"`
		Publish struct {
			If string `yaml:"if"`
		} `yaml:"publish"`
		Rebuild struct {
			If    string        `yaml:"if"`
			Steps []releaseStep `yaml:"steps"`
		} `yaml:"rebuild-assets"`
	} `yaml:"jobs"`
}

type releaseStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

func readReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func TestReleaseWorkflowDispatchModes(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	if len(workflow.On.WorkflowRun.Workflows) != 1 || workflow.On.WorkflowRun.Workflows[0] != "CI" ||
		len(workflow.On.WorkflowRun.Branches) != 1 || workflow.On.WorkflowRun.Branches[0] != "main" ||
		len(workflow.On.WorkflowRun.Types) != 1 || workflow.On.WorkflowRun.Types[0] != "completed" {
		t.Fatalf("workflow_run trigger = %#v, want CI completed runs on main", workflow.On.WorkflowRun)
	}
	if workflow.On.WorkflowDispatch.Inputs.Tag.Required || workflow.On.WorkflowDispatch.Inputs.Tag.Default != "" {
		t.Fatal("manual tag must be optional so an empty main dispatch can prepare a release")
	}
	for _, fragment := range []string{
		"github.event_name == 'workflow_dispatch'",
		"github.ref == 'refs/heads/main'",
		"inputs.tag == ''",
		"github.event_name == 'workflow_run'",
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.event == 'push'",
		"github.event.workflow_run.head_branch == 'main'",
	} {
		if !strings.Contains(workflow.Jobs.Prepare.If, fragment) {
			t.Fatalf("prepare condition %q does not contain %q", workflow.Jobs.Prepare.If, fragment)
		}
	}
	var prepareCheckoutRef string
	for _, step := range workflow.Jobs.Prepare.Steps {
		if step.Name == "Checkout" {
			prepareCheckoutRef = step.With["ref"]
			break
		}
	}
	if prepareCheckoutRef != "${{ github.event_name == 'workflow_dispatch' && 'main' || github.event.workflow_run.head_sha }}" {
		t.Fatalf("prepare checkout ref = %q, want event-safe main/SHA selection", prepareCheckoutRef)
	}
	for _, fragment := range []string{
		"github.event_name == 'workflow_run'",
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.event == 'push'",
		"github.event.workflow_run.head_branch == 'main'",
		"startsWith(github.event.workflow_run.head_commit.message, 'chore(release):')",
	} {
		if !strings.Contains(workflow.Jobs.Publish.If, fragment) {
			t.Fatalf("publish condition %q does not contain %q", workflow.Jobs.Publish.If, fragment)
		}
	}
}

func rebuildMetadataScript(t *testing.T) string {
	t.Helper()
	workflow := readReleaseWorkflow(t)
	if workflow.Jobs.Rebuild.If != "github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main' && inputs.tag != ''" {
		t.Fatalf("rebuild-assets condition = %q, want tagged main workflow dispatch", workflow.Jobs.Rebuild.If)
	}
	validateIndex, checkoutIndex := -1, -1
	validateRun, checkoutRef := "", ""
	for index, step := range workflow.Jobs.Rebuild.Steps {
		switch step.Name {
		case "Validate rebuild tag input":
			validateIndex = index
			validateRun = step.Run
		case "Checkout tag":
			checkoutIndex = index
			checkoutRef = step.With["ref"]
		}
	}
	if validateIndex < 0 || checkoutIndex <= validateIndex {
		t.Fatal("rebuild tag validation must precede tag checkout")
	}
	if !strings.Contains(validateRun, `[[ ! "$REBUILD_TAG" =~ ^v`) {
		t.Fatal("rebuild tag validation is missing its semantic-version guard")
	}
	if checkoutRef != "${{ env.REBUILD_TAG }}" {
		t.Fatalf("rebuild checkout ref = %q, want validated environment tag", checkoutRef)
	}
	for _, step := range workflow.Jobs.Rebuild.Steps {
		if step.ID == "meta" {
			if !strings.Contains(step.Run, `test "$(git log -1 --format=%s HEAD)" = "chore(release): hooversion ${version}"`) {
				t.Fatal("rebuild metadata does not require the exact release commit subject")
			}
			return step.Run
		}
	}
	t.Fatal("rebuild-assets metadata step missing")
	return ""
}

func seedRebuildWorkflowRepo(t *testing.T, movedTag bool) string {
	t.Helper()
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "VERSION"), "1.1.0\n")
	commitAll(t, cwd, "chore(release): hooversion 1.1.0")
	gitOut(t, cwd, "tag", "-a", "v1.1.0", "-m", "hooversion 1.1.0")
	if movedTag {
		writeFile(t, filepath.Join(cwd, "follow-up.txt"), "same version, later main commit\n")
		commitAll(t, cwd, "chore(ci): enable manual CI recovery")
		gitOut(t, cwd, "tag", "-f", "v1.1.0")
	}

	remote := makeBareRemote(t)
	gitOut(t, cwd, "remote", "add", "origin", remote)
	gitOut(t, cwd, "push", "origin", "main")
	gitOut(t, cwd, "checkout", "--detach", "v1.1.0")
	return cwd
}

func fakeGH(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func runRebuildMetadata(t *testing.T, cwd, script string) (string, error) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "github-output")
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"GH_TOKEN=test-token",
		"REBUILD_TAG=v1.1.0",
		"GITHUB_OUTPUT="+output,
		"PATH="+fakeGH(t)+":"+os.Getenv("PATH"),
	)
	err := cmd.Run()
	data, readErr := os.ReadFile(output)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	return string(data), err
}

func TestRebuildWorkflowAcceptsExactReleaseCommit(t *testing.T) {
	script := rebuildMetadataScript(t)
	output, err := runRebuildMetadata(t, seedRebuildWorkflowRepo(t, false), script)
	if err != nil {
		t.Fatalf("valid release commit rejected: %v", err)
	}
	if !strings.Contains(output, "version=1.1.0\n") || !strings.Contains(output, "tag=v1.1.0\n") {
		t.Fatalf("metadata output = %q, want version and tag", output)
	}
}

func TestRebuildWorkflowRejectsMovedTagWithSameVersion(t *testing.T) {
	script := rebuildMetadataScript(t)
	output, err := runRebuildMetadata(t, seedRebuildWorkflowRepo(t, true), script)
	if err == nil {
		t.Fatal("moved tag on a later same-version commit was accepted")
	}
	if output != "" {
		t.Fatalf("moved tag emitted metadata before rejection: %q", output)
	}
}
