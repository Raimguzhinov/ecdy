// Package shell embeds the shell integration scripts served by `ecdy init`.
package shell

import _ "embed"

// Zsh is shell/zsh/ecdy.plugin.zsh, printed by `ecdy init zsh`.
//
//go:embed zsh/ecdy.plugin.zsh
var Zsh string
