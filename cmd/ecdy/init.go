package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Raimguzhinov/ecdy/shell"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init zsh",
		Short: "Print the shell integration script",
		Long: "init prints the integration script for the given shell. Add this to the\n" +
			"end of ~/.zshrc (after other plugins that wrap accept-line):\n\n" +
			"  eval \"$(ecdy init zsh)\"",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"zsh"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "zsh" {
				return fmt.Errorf("unsupported shell %q (only zsh is supported)", args[0])
			}
			if _, err := io.WriteString(cmd.OutOrStdout(), shell.Zsh); err != nil {
				return fmt.Errorf("write script: %w", err)
			}
			return nil
		},
	}
}
