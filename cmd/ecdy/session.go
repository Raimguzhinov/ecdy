package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Raimguzhinov/ecdy/internal/daemon"
)

func newUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use [AGENT]",
		Short: "Choose the agent of this shell session",
		Long: "use makes AGENT the agent for the next prompts of this shell session.\n" +
			"Each agent keeps its own conversation while the session's daemon runs.\n" +
			"Without AGENT, use prints the session's agent.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := sessionPaths()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), currentAgent(p))
				return nil
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, _, err := cfg.Agent(args[0])
			if err != nil {
				return err
			}
			if _, err := daemon.UpdateState(p.State, func(s *daemon.State) { s.Agent = name }); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "using %s\n", name)
			return nil
		},
	}
}

func newNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new",
		Short: "Start a new conversation in this shell session",
		Long: "new makes the next prompt to each agent of this shell session start a new\n" +
			"ACP session in the current directory of that prompt. The agents keep\n" +
			"running.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := sessionPaths()
			if err != nil {
				return err
			}
			if _, err := daemon.UpdateState(p.State, func(s *daemon.State) { s.Generation++ }); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "the next prompt starts a new conversation")
			return nil
		},
	}
}
