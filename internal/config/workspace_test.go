package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkdirDefaultsToLaunchDirectory(t *testing.T) {
	t.Setenv("HARNESS_WORKDIR", "")
	t.Chdir(t.TempDir())
	want, _ := os.Getwd()
	c, err := Load()
	if err != nil || c.Workdir != want {
		t.Fatalf("workdir=%q err=%v want launch directory %q", c.Workdir, err, want)
	}
	t.Setenv("HARNESS_WORKDIR", "other-project")
	c, err = Load()
	if err != nil || c.Workdir != filepath.Join(want, "other-project") {
		t.Fatalf("env workdir=%q err=%v", c.Workdir, err)
	}
}

func TestWorkdirFlagOverridesEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	t.Setenv("HARNESS_WORKDIR", "env-project")
	c, err := LoadWithWorkdir("flag-project")
	if err != nil || c.Workdir != filepath.Join(cwd, "flag-project") {
		t.Fatalf("flag workdir=%q err=%v", c.Workdir, err)
	}
	absolute := t.TempDir()
	c, err = LoadWithWorkdir(absolute)
	if err != nil || c.Workdir != absolute {
		t.Fatalf("absolute flag workdir=%q err=%v", c.Workdir, err)
	}
}

func TestMisePreservesLaunchDirectory(t *testing.T) {
	mise, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("mise unavailable")
	}
	root, _ := filepath.Abs("../..")
	t.Setenv("HARNESS_WORKDIR", "")
	t.Setenv("MISE_CONFIG_FILE", filepath.Join(root, "mise.toml"))
	dir := t.TempDir()
	cmd := exec.Command(mise, "exec", "--", "sh", "-c", `printf '%s' "${HARNESS_WORKDIR:-$PWD}"`)
	cmd.Dir = dir
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HARNESS_WORKDIR=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mise: %v: %s", err, out)
	}
	got, _ := filepath.EvalSymlinks(string(out))
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("mise workdir=%q want %q", out, dir)
	}
}
