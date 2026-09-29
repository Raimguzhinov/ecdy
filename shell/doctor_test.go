package shell_test

import (
	"os"
	"testing"
)

// TestDoctor: `ecdy doctor` typed at the prompt checks this shell, and finds
// what other plugins took from ecdy.
func TestDoctor(t *testing.T) { forEachZsh(t, testDoctor) }

func testDoctor(t *testing.T, zsh string) {
	fzfTab := os.Getenv("ECDY_TEST_FZF_TAB")
	if fzfTab == "" {
		t.Skip("ECDY_TEST_FZF_TAB is not set (run the tests in `nix develop`)")
	}
	for _, tt := range []struct {
		name  string
		opts  zshOpts
		wants []string
	}{
		{"healthy", zshOpts{}, []string{
			"✓ plugin: loaded in this shell", "✓ Enter: classified by ecdy", "✓ Alt+Enter: runs the line as a command",
			"✓ ? prefix: sends the line to the agent", "✓ indicator: shown as rprompt",
		}},
		{"accept-line reset", zshOpts{after: "zle -A .accept-line accept-line"}, []string{"✗ Enter: accept-line is builtin"}},
		{"Alt+Enter taken", zshOpts{after: "bindkey '^[^M' undefined-key"}, []string{"! Alt+Enter: runs undefined-key"}},
		{"atuin with AI", zshOpts{after: `eval "$(atuin init zsh)"`}, []string{
			"! ? prefix: ? on an empty line starts atuin's AI", "--disable-ai",
			"! atuin: atuin records every prompt as `ecdy ask -- '…'`",
		}},
		{"fzf-tab accept-line", zshOpts{after: "autoload -Uz compinit && compinit -u\nsource " + fzfTab + "\nzstyle ':fzf-tab:*' accept-line enter"}, []string{
			"✗ fzf-tab: its accept-line key (enter) runs the line without ecdy's classification",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			z := startZsh(t, zsh, tt.opts)
			z.Send("NO_COLOR=1 ecdy doctor; print -r -- doctor-exit-$?" + enter)
			for _, w := range tt.wants {
				z.Expect(w)
			}
			z.Expect("doctor-exit-")
			z.ExpectPrompt()
		})
	}
}
