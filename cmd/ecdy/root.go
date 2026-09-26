package main

import "github.com/spf13/cobra"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ecdy",
		Short: "Type in zsh; prompts go to your ACP agent",
		Long: "ecdy sits on top of your zsh: on Enter it classifies the line as a command\n" +
			"or a prompt, runs commands as usual and sends prompts to any agent that\n" +
			"speaks the Agent Client Protocol (ACP).",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCmd())
	return root
}
