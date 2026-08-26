// Legacy TypeScript-config migration; mirrors the intent of importing a
// hooversion.config.{ts,mjs,js,cjs} module (module.default ?? module.config
// ?? module) via a bun shell-out, as specified by hv-go-contract.md.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/types"
)

// bunLookPath and bunRun are seams overridable in tests so the no-bun error
// branch and JSON conversion are deterministic without requiring bun.
var (
	bunLookPath = exec.LookPath
	bunRun      = func(dir, script string, extraEnv []string) ([]byte, error) {
		cmd := exec.Command("bun", "-e", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), extraEnv...)
		out, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return out, fmt.Errorf("%v: %s", exitErr, exitErr.Stderr)
			}
			return out, err
		}
		return out, nil
	}
)

const migrateScript = `
const { pathToFileURL } = await import("node:url");
const mod = await import(pathToFileURL(process.env.HOOVERSION_MIGRATE_PATH).href + "?t=" + Date.now());
const raw = mod.default ?? mod.config ?? mod;
process.stdout.write(JSON.stringify(raw));
`

// MigrateFromTS imports tsPath with bun, normalizes it, and writes the result
// as hooversion.yaml in cwd. Without bun installed it fails with manual
// migration guidance.
func MigrateFromTS(cwd, tsPath string) (*types.NormalizedConfig, string, error) {
	if _, err := bunLookPath("bun"); err != nil {
		return nil, "", errors.New(
			"%s is a legacy Hooversion config; migrating it requires bun. Install bun (https://bun.sh) and rerun `hooversion migrate`, or manually convert the config to hooversion.yaml.",
			tsPath)
	}

	abs := tsPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, tsPath)
	}
	out, err := bunRun(cwd, migrateScript, []string{"HOOVERSION_MIGRATE_PATH=" + abs})
	if err != nil {
		return nil, "", errors.New("Migrating %s failed: %v", tsPath, err)
	}

	var raw types.Config
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, "", errors.New("Migrating %s produced invalid JSON: %v", tsPath, err)
	}

	cfg, err := Normalize(cwd, &raw)
	if err != nil {
		return nil, "", err
	}

	yamlPath := filepath.Join(cwd, "hooversion.yaml")
	if err := os.WriteFile(yamlPath, renderYAML(cfg), 0o644); err != nil {
		return nil, "", err
	}
	return cfg, yamlPath, nil
}
