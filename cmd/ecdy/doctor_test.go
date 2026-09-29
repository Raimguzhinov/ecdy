package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

// runDoctorCmd runs `ecdy doctor args...` with config as the config file and
// returns its output and exit code.
func runDoctorCmd(t *testing.T, config string, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config", "ecdy", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	cmd := exec.Command(self, append([]string{"doctor"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{
		"ECDY_TEST_MAIN=1",
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"PATH=" + os.Getenv("PATH"),
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

// fakeAgents returns config lines for fake agents running the scripts.
func fakeAgents(t *testing.T, scripts map[string]fakeagent.Script) string {
	t.Helper()
	self, _ := os.Executable()
	toml := "default_agent = \"ok\"\n"
	for name, s := range scripts {
		dir := t.TempDir()
		scriptEnv, err := fakeagent.WriteScript(dir, s)
		if err != nil {
			t.Fatal(err)
		}
		toml += "[agents." + name + "]\ncommand = [\"env\", " + quote(scriptEnv) + ", " + quote(self) + ", \"fake-agent\"]\n"
	}
	return toml
}

func TestDoctorOffline(t *testing.T) {
	toml := "default_agent = \"a\"\n[agents.a]\ncommand = [\"sh\"]\n[agents.b]\ncommand = [\"no-such-agent-cmd\"]\n"
	out, code := runDoctorCmd(t, toml)
	for _, want := range []string{"✓ config: ", "✓ agent a: sh (default)", "! agent b: `no-such-agent-cmd` not found on $PATH", "! plugin: not run from a zsh with the ecdy plugin"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if code != 0 || strings.Contains(out, "login") {
		t.Errorf("exit %d (warnings only, no agent started):\n%s", code, out)
	}
	// A missing default agent fails.
	out, code = runDoctorCmd(t, "default_agent = \"b\"\n[agents.b]\ncommand = [\"no-such-agent-cmd\"]\n")
	if code != 1 || !strings.Contains(out, "✗ agent b:") {
		t.Errorf("exit %d:\n%s", code, out)
	}
	// So does a broken config.
	out, code = runDoctorCmd(t, "default_agent = [\n")
	if code != 1 || !strings.Contains(out, "✗ config:") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// With --agents every agent is started: initialize and session/new, no
// prompt; a login it needs, a crash and a silent agent are failures.
func TestDoctorAgents(t *testing.T) {
	toml := fakeAgents(t, map[string]fakeagent.Script{
		"ok":   {},
		"auth": {AuthRequired: true},
	})
	toml += "[agents.silent]\ncommand = [\"sleep\", \"30\"]\n[agents.crash]\ncommand = [\"false\"]\n"
	start := time.Now()
	out, code := runDoctorCmd(t, toml, "--agents", "--timeout", "1s")
	for _, want := range []string{
		"✓ login ok: started, session created",
		"✗ login auth: agent requires authentication (Log in with a browser; Log in in a terminal)",
		"✗ login silent: no answer within 1s",
		"✗ login crash: agent exited",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s: the agents are not checked in parallel, or the timeout does not stop them", d)
	}
	// --agent: only that one.
	out, code = runDoctorCmd(t, toml, "--agent", "ok")
	if code != 0 || !strings.Contains(out, "login ok") || strings.Contains(out, "login auth") {
		t.Errorf("exit %d:\n%s", code, out)
	}
	if _, code := runDoctorCmd(t, toml, "--agent", "nope"); code != 1 {
		t.Errorf("unknown agent: exit %d", code)
	}
}
