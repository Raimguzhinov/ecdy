package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"

	"github.com/Raimguzhinov/ecdy/internal/acpclient"
	"github.com/Raimguzhinov/ecdy/internal/config"
	"github.com/Raimguzhinov/ecdy/internal/doctor"
)

func newDoctorCmd() *cobra.Command {
	var (
		agents  bool
		only    []string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "doctor [--agents [--agent NAME]...]",
		Short: "Check that ecdy can work",
		Long: "doctor checks the ecdy binary the plugin runs, the config, that every\n" +
			"agent's command is on $PATH, zsh, and, when typed at the prompt of a zsh\n" +
			"with the plugin, the keys and widgets other plugins may have taken\n" +
			"(Enter, the force key, the ? prefix, the indicator, atuin, fzf-tab).\n\n" +
			"With --agents it also starts each agent (initialize and session/new,\n" +
			"no prompt) to see that it runs and that you are logged in. That starts\n" +
			"the agents' processes, which may use the network (npx downloads them).\n\n" +
			"Exit status: 0 nothing failed (warnings allowed), 1 something failed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd, agents || len(only) > 0, only, timeout)
		},
	}
	cmd.Flags().BoolVar(&agents, "agents", false, "start every agent to check it runs and is logged in")
	cmd.Flags().StringSliceVarP(&only, "agent", "a", nil, "start only this agent (repeatable; implies --agents)")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "how long each agent may take to start")
	return cmd
}

// envDoctorZsh carries what the plugin knows about the shell
// (_ecdy_doctor_probe in shell/zsh/ecdy.plugin.zsh).
const envDoctorZsh = "ECDY_DOCTOR_ZSH"

func runDoctor(cmd *cobra.Command, agents bool, only []string, timeout time.Duration) error {
	ctx := cmd.Context()
	e := doctorEnv(ctx)
	for _, n := range only {
		if _, ok := e.Config.Agents[n]; !ok && e.ConfigErr == nil {
			return fmt.Errorf("unknown agent %q", n)
		}
	}
	rs := doctor.Check(e)
	if agents && e.ConfigErr == nil {
		rs = append(rs, checkLogins(ctx, e.Config, only, timeout)...)
	}
	stdout := cmd.OutOrStdout()
	color := isTerminal(stdout) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	doctor.Format(stdout, rs, color)
	for _, r := range rs {
		if r.Status == doctor.Fail {
			return exitCode(1, nil)
		}
	}
	return nil
}

func doctorEnv(ctx context.Context) doctor.Env {
	e := doctor.Env{
		Version:  resolveVersion(),
		LookPath: exec.LookPath,
		SameFile: func(a, b string) bool {
			fa, errA := os.Stat(a)
			fb, errB := os.Stat(b)
			return errA == nil && errB == nil && os.SameFile(fa, fb)
		},
		ZshVersion: func() (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "zsh", "-fc", `print -r -- $ZSH_VERSION`).Output()
			return strings.TrimSpace(string(out)), err
		},
		Shell:    os.Getenv(envDoctorZsh),
		Getenv:   os.Getenv,
		ReadFile: os.ReadFile,
	}
	e.Self, _ = os.Executable()
	if p, err := filepath.EvalSymlinks(e.Self); err == nil {
		e.Self = p
	}
	e.Home, _ = os.UserHomeDir()
	e.ConfigPath, e.ConfigErr = config.Path()
	if e.ConfigErr == nil {
		if _, err := os.Stat(e.ConfigPath); errors.Is(err, fs.ErrNotExist) {
			e.ConfigMissing = true
		}
		e.Config, e.ConfigErr = config.Load(e.ConfigPath)
	}
	return e
}

// checkLogins starts the agents (all, or the ones named) in parallel, each
// for initialize and session/new only, and closes them. Agents whose
// command is not found are skipped: the offline checks report them.
func checkLogins(ctx context.Context, cfg config.Config, only []string, timeout time.Duration) []doctor.Result {
	var names []string
	for n, a := range cfg.Agents {
		if len(only) > 0 && !slices.Contains(only, n) {
			continue
		}
		if _, err := exec.LookPath(a.Command[0]); err != nil {
			continue
		}
		names = append(names, n)
	}
	slices.Sort(names)
	dir, err := os.Getwd()
	if err != nil {
		dir = "/"
	}
	rs := make([]doctor.Result, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			c, err := acpclient.Start(ctx, acpclient.Options{
				Command:       cfg.Agents[n].Command,
				Dir:           dir,
				KillGrace:     killGrace,
				ClientVersion: resolveVersion(),
			}, noTurn{})
			if err == nil {
				c.Close()
			} else if ctx.Err() != nil {
				err = context.DeadlineExceeded
			}
			rs[i] = doctor.Login(n, err, timeout)
		})
	}
	wg.Wait()
	return rs
}

// noTurn handles a connection that never has a turn.
type noTurn struct{}

func (noTurn) SessionUpdate(context.Context, acp.SessionUpdate) {}

func (noTurn) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
	return acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}, nil
}
