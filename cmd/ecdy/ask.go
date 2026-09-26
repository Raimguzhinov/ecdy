package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// newAskCmd is a stub until M3 connects it to an ACP agent: it only prints
// the prompt, so the zsh integration can be tested end to end.
func newAskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ask [--] PROMPT",
		Short: "Send a prompt to the agent (stub: prints the prompt)",
		Long: "ask sends PROMPT to the agent. The zsh plugin runs it for lines the\n" +
			"classifier recognizes as prompts. Not connected to an agent yet: for\n" +
			"now it prints the prompt it would send.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := strings.Join(args, " ")
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "ecdy ask (no agent yet): %s\n", prompt); err != nil {
				return fmt.Errorf("write prompt: %w", err)
			}
			return nil
		},
	}
}
