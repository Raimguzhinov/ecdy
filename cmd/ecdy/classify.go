package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Raimguzhinov/ecdy/internal/classify"
)

func newClassifyCmd() *cobra.Command {
	var (
		shell     string
		firstKind string
		cwd       string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "classify [flags] -- LINE",
		Short: "Classify a line as cmd, prompt or ask",
		Long: "classify prints the verdict for LINE: \"cmd\" (run it in the shell),\n" +
			"\"prompt\" (send it to the agent) or \"ask\" (let the user choose).\n\n" +
			"The shell plugin passes --first-kind from `whence -w`, since only the shell\n" +
			"knows its aliases and functions. Without it (\"auto\"), ecdy guesses from\n" +
			"a list of zsh builtins and reserved words and from $PATH.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shell != "zsh" {
				return fmt.Errorf("unsupported shell %q (only zsh is supported)", shell)
			}
			line := strings.Join(args, " ")
			var kind classify.Kind
			if firstKind == "auto" {
				kind = guessKind(classify.FirstWord(line))
			} else {
				k, err := classify.ParseKind(firstKind)
				if err != nil {
					return fmt.Errorf("--first-kind: %w", err)
				}
				kind = k
			}
			if cwd == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				cwd = wd
			}
			res := classify.Classify(classify.Input{
				Line:      line,
				FirstKind: kind,
				FS:        os.DirFS(cwd).(fs.StatFS),
			})
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetEscapeHTML(false)
				if err := enc.Encode(res); err != nil {
					return fmt.Errorf("write result: %w", err)
				}
				return nil
			}
			if _, err := fmt.Fprintln(out, res.Verdict); err != nil {
				return fmt.Errorf("write result: %w", err)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&shell, "shell", "zsh", "shell the line was typed in")
	f.StringVar(&firstKind, "first-kind", "auto",
		"kind of the first word as printed by `whence -w`: alias, function, builtin, command, reserved, hashed, none, or auto")
	f.StringVar(&cwd, "cwd", "", "directory used to tell file names from words (default: current directory)")
	f.BoolVar(&asJSON, "json", false, "print the verdict, reason and signals as JSON")
	return cmd
}

// zshReserved and zshBuiltins back --first-kind=auto, which exists for manual
// use; the plugin always passes the real kind.
var (
	zshReserved = strings.Fields(`! [[ { } case coproc do done elif else end esac fi for foreach
		function if in nocorrect repeat select then time until while`)
	zshBuiltins = strings.Fields(`. : alias autoload bg bindkey break builtin bye cd chdir command
		compadd continue declare dirs disable disown echo emulate enable eval exec exit export false
		fc fg float functions getopts hash history integer jobs kill let limit local logout noglob
		popd print printf pushd pwd r read readonly rehash return set setopt shift source suspend
		test times trap true ttyctl type typeset ulimit umask unalias unfunction unhash unlimit unset
		unsetopt vared wait whence where which zle zmodload zparseopts zstyle`)
)

func guessKind(word string) classify.Kind {
	switch {
	case word == "":
		return classify.KindNone
	case slices.Contains(zshReserved, word):
		return classify.KindReserved
	case slices.Contains(zshBuiltins, word):
		return classify.KindBuiltin
	}
	if _, err := exec.LookPath(word); err == nil {
		return classify.KindCommand
	}
	return classify.KindNone
}
