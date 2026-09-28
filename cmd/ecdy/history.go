package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Raimguzhinov/ecdy/internal/config"
	"github.com/Raimguzhinov/ecdy/internal/sessionlog"
)

// sessionLog returns the command log of session
// (docs/adr/0004-session-context.md).
func sessionLog(session string) (*sessionlog.Log, error) {
	dir, err := sessionlog.Dir()
	if err != nil {
		return nil, err
	}
	return sessionlog.Open(dir, session)
}

// buildContext is the context block for a prompt in cwd, for the daemon
// and for `ecdy log --context` alike.
func buildContext(cfg config.Config, cwd string, recs []sessionlog.Record, since time.Time) (string, time.Time) {
	return sessionlog.BuildContext(sessionlog.ContextOptions{
		Cwd: cwd, Branch: sessionlog.GitBranch(cwd), Records: recs, Since: since, Commands: cfg.ContextCommands,
	})
}

// removeSessionLog deletes the command log of session, if any.
func removeSessionLog(session string) {
	if l, err := sessionLog(session); err == nil {
		_ = l.Remove()
	}
}

func newLogCmd() *cobra.Command {
	var asJSON, asContext bool
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show the commands of this shell session that the agent can see",
		Long: "log prints the command log of this shell session: every command with its\n" +
			"exit status, duration and directory, secrets already redacted. With the\n" +
			"last `[context] commands` of them (config.toml, default 20), the current\n" +
			"directory and the git branch, it is sent to the agent with a prompt; each\n" +
			"turn of a conversation gets only the commands run since the previous one.\n" +
			"--context prints that block as the first prompt of a conversation here\n" +
			"would get it. Command output is never recorded.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := sessionPaths()
			if err != nil {
				return err
			}
			l, err := sessionLog(p.Session)
			if err != nil {
				return err
			}
			recs, err := l.Read()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			switch {
			case asContext:
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("current directory: %w", err)
				}
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				text, _ := buildContext(cfg, cwd, recs, time.Time{})
				_, _ = fmt.Fprint(w, text)
			case asJSON:
				enc := json.NewEncoder(w)
				for _, r := range recs {
					if err := enc.Encode(r); err != nil {
						return fmt.Errorf("write record: %w", err)
					}
				}
			default:
				for _, r := range recs {
					line := strings.ReplaceAll(r.Line, "\n", "\n    ")
					_, _ = fmt.Fprintf(w, "%s  exit %-3d %8s  %s  %s\n", r.Start.Local().Format("15:04:05"),
						r.Exit, r.Dur.Round(time.Millisecond), r.Cwd, line)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the records as JSON lines")
	cmd.Flags().BoolVar(&asContext, "context", false, "print the context block a new conversation would get here")
	cmd.MarkFlagsMutuallyExclusive("json", "context")
	cmd.AddCommand(newLogRecordCmd())
	return cmd
}

// newLogRecordCmd is what the zsh plugin's precmd hook runs, in the
// background, after every command.
func newLogRecordCmd() *cobra.Command {
	var (
		exit       int
		start, end string
		cwd        string
	)
	cmd := &cobra.Command{
		Use:    "record --exit N --start T --end T --cwd DIR -- LINE",
		Short:  "Add a command to the session's log (run by the zsh plugin)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			p, err := sessionPaths()
			if err != nil {
				return err
			}
			t0, err := epoch(start)
			if err != nil {
				return fmt.Errorf("--start: %w", err)
			}
			t1, err := epoch(end)
			if err != nil {
				return fmt.Errorf("--end: %w", err)
			}
			l, err := sessionLog(p.Session)
			if err != nil {
				return err
			}
			return l.Append(sessionlog.Record{Line: args[0], Cwd: cwd, Exit: exit, Start: t0, Dur: max(t1.Sub(t0), 0)})
		},
	}
	cmd.Flags().IntVar(&exit, "exit", 0, "exit status")
	cmd.Flags().StringVar(&start, "start", "", "start time, seconds since the epoch ($EPOCHREALTIME)")
	cmd.Flags().StringVar(&end, "end", "", "end time, seconds since the epoch")
	cmd.Flags().StringVar(&cwd, "cwd", "", "directory the command started in")
	for _, f := range []string{"start", "end"} {
		_ = cmd.MarkFlagRequired(f)
	}
	return cmd
}

// epoch parses zsh's $EPOCHREALTIME: seconds since the epoch, a decimal
// point (',' in some locales) and a fraction. zsh prints it as a float, with
// more digits than it has (1790624277.7849361897): past nanoseconds they are
// dropped.
func epoch(s string) (time.Time, error) {
	bad := errors.New("not seconds since the epoch: " + strconv.Quote(s))
	secs, frac, _ := strings.Cut(strings.Replace(s, ",", ".", 1), ".")
	sec, err := strconv.ParseUint(secs, 10, 40)
	if err != nil || strings.Trim(frac, "0123456789") != "" {
		return time.Time{}, bad
	}
	frac = (frac + "000000000")[:9]
	nsec, _ := strconv.ParseUint(frac, 10, 32) // nine digits
	return time.Unix(int64(sec), int64(nsec)).UTC(), nil
}
