package shell_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompat: ecdy next to plugins that take over keys, widgets or hooks
// (AGENTS.md, section 3), loaded before and after it. The findings are
// what `ecdy doctor` checks for.
func TestCompat(t *testing.T) { forEachZsh(t, testCompat) }

func testCompat(t *testing.T, zsh string) {
	env := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			t.Skipf("%s is not set (run the tests in `nix develop`)", name)
		}
		return v
	}
	for _, p := range []struct {
		name, load string
		check      func(z *zshTerm)
	}{
		{
			// Initializes itself at the first prompt: bindkey -v, its own
			// zle-line-* and reset-prompt widgets, KEYTIMEOUT=1.
			name: "zsh-vi-mode",
			load: "source " + env("ECDY_TEST_ZSH_VI_MODE"),
			check: func(z *zshTerm) {
				z.Run(`print -r -- vi-$ZVM_INIT_DONE-${${(M)$(bindkey -lL main)#*viins}:+main}`, "vi-true-main")
			},
		},
		{
			name: "fzf-tab",
			load: "autoload -Uz compinit && compinit -u\nsource " + env("ECDY_TEST_FZF_TAB"),
			check: func(z *zshTerm) {
				z.Run(`bindkey '^I'`, `"^I" fzf-tab-complete`)
			},
		},
		{
			// --disable-ai: atuin binds ? to its own AI mode otherwise,
			// which takes ecdy's ? prefix (checked by `ecdy doctor`).
			name: "atuin",
			load: `eval "$(atuin init zsh --disable-ai)"`,
			check: func(z *zshTerm) {
				z.Run(`bindkey '^R'`, `"^R" atuin-search`)
				// With the filter the README recommends, atuin does not
				// record the rewritten line of a prompt.
				z.Run(`atuin history list --cmd-only | grep -c '^ecdy ask' ; print -r -- listed`, "0\r\nlisted")
			},
		},
	} {
		for _, order := range []string{"before", "after"} {
			t.Run(p.name+"/"+order, func(t *testing.T) {
				o := zshOpts{}
				if order == "before" {
					o.rc = p.load
				} else {
					o.after = p.load
				}
				home := t.TempDir()
				cfg := filepath.Join(home, "atuin")
				if err := os.MkdirAll(cfg, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("history_filter = [\"^ecdy ask -- \"]\nauto_sync = false\nupdate_check = false\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				o.rc = "export ATUIN_CONFIG_DIR=" + shellQuote(cfg) + "\n" + o.rc
				z := startZsh(t, zsh, o)
				z.Run(`print -r -- cmd-$((6*7))`, "cmd-42")
				z.Run("explain this error", askReply+"explain this error")
				// The indicator, while typing.
				z.Send("explain again")
				z.Expect("→ agent")
				z.Send(ctrlU + `print -r -- after-$((6*7))` + enter)
				z.Expect("after-42")
				z.ExpectPrompt()
				// Ask: Esc drops the line.
				z.Send("rm everything in tmp except configs" + enter)
				z.Expect("destructive command")
				z.Send(esc)
				z.ExpectPrompt()
				// The force key runs the line as a command.
				z.Send("explain this error" + forceKey)
				z.Expect("command not found: explain")
				z.ExpectPrompt()
				p.check(z)
				z.Send("exit" + enter)
				z.Wait()
				data, err := os.ReadFile(filepath.Join(z.home, ".zsh_history"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "ecdy ask --") || !strings.Contains(string(data), "explain this error") {
					t.Errorf("history file:\n%s", data)
				}
			})
		}
	}
}
