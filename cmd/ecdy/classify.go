package main

import (
	"encoding/json"
	"fmt"
	"io"
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
		format    string
		comments  bool
	)
	cmd := &cobra.Command{
		Use:   "classify [flags] -- LINE",
		Short: "Classify a line as cmd, prompt or ask",
		Long: "classify prints the verdict for LINE: \"cmd\" (run it in the shell),\n" +
			"\"prompt\" (send it to the agent) or \"ask\" (let the user choose).\n\n" +
			"The shell plugin passes --first-kind from `whence -w`, since only the shell\n" +
			"knows its aliases and functions. Without it (\"auto\"), ecdy guesses from\n" +
			"a list of zsh builtins and reserved words and from $PATH.\n\n" +
			"With --comments, '#' at the start of a word starts a comment, as with zsh's\n" +
			"INTERACTIVE_COMMENTS option; the plugin passes it when the option is set.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch format {
			case "text", "json", "nul":
			default:
				return fmt.Errorf("--format: unknown format %q (want text, json or nul)", format)
			}
			if asJSON {
				format = "json"
			}
			if shell != "zsh" {
				return fmt.Errorf("unsupported shell %q (only zsh is supported)", shell)
			}
			line := strings.Join(args, " ")
			var kind classify.Kind
			if firstKind == "auto" {
				kind = guessKind(classify.FirstWord(line, comments))
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
				Comments:  comments,
			})
			return writeResult(cmd.OutOrStdout(), format, res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&shell, "shell", "zsh", "shell the line was typed in")
	f.StringVar(&firstKind, "first-kind", "auto",
		"kind of the first word as printed by `whence -w`: alias, function, builtin, command, reserved, hashed, none, or auto")
	f.StringVar(&cwd, "cwd", "", "directory used to tell file names from words (default: current directory)")
	f.BoolVar(&comments, "comments", false, "treat '#' at the start of a word as a comment (zsh's INTERACTIVE_COMMENTS)")
	f.BoolVar(&asJSON, "json", false, "same as --format=json")
	f.StringVar(&format, "format", "text",
		"output format: text (the verdict), json (verdict, reason, signals), or nul (for the shell plugin)")
	return cmd
}

// writeResult prints res in the given format. The nul format is what the
// shell plugin reads: five NUL-terminated fields, in this order: verdict,
// prompt, suggestion, correction, dangerous ("1" or "0"). NUL is the one byte
// that cannot occur in a shell line, so the fields need no escaping.
func writeResult(w io.Writer, format string, res classify.Result) error {
	var err error
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		err = enc.Encode(res)
	case "nul":
		dangerous := "0"
		if res.Dangerous {
			dangerous = "1"
		}
		fields := []string{res.Verdict.String(), res.Prompt, res.Suggestion, res.Correction, dangerous}
		_, err = io.WriteString(w, strings.Join(fields, "\x00")+"\x00")
	default:
		_, err = fmt.Fprintln(w, res.Verdict)
	}
	if err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
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
