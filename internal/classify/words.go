package classify

// Word lists used by the heuristics. All entries are lowercase.

// strongStopWords almost never appear as command arguments but are very
// common in natural language. Each distinct one adds weightStrong.
var strongStopWords = set(
	// en: articles, pronouns, question words, auxiliaries
	"the", "this", "that", "these", "those", "it", "its", "it's",
	"me", "my", "mine", "we", "our", "us", "you", "your", "i'm", "i've",
	"he", "she", "they", "them", "their",
	"why", "how", "what", "what's", "which", "where", "when", "whats",
	"is", "are", "was", "were", "be", "been", "am",
	"does", "did", "doesn't", "don't", "isn't", "aren't", "didn't", "won't", "can't",
	"should", "could", "would", "can", "will", "must", "might",
	"please", "pls", "plz", "thanks",
	"every", "everything", "anything", "something", "nothing", "everywhere",
	"except", "than", "because", "about", "there", "here", "why's",
	// ru
	"как", "что", "почему", "зачем", "где", "когда", "какой", "какие",
	"все", "всё", "кроме", "пожалуйста", "это", "этот", "эту", "эти",
	"мне", "мой", "мою", "мои", "я", "ты", "вы", "и", "в", "на", "не", "с",
	"для", "из", "по", "или", "но", "чтобы",
)

// weakStopWords are frequent in natural language but also plausible as
// arguments ("make all", "docker compose up", "for f in ..."). Each distinct
// one adds weightWeak.
var weakStopWords = set(
	"a", "an", "all", "and", "or", "not", "no", "in", "on", "to", "for",
	"with", "without", "of", "by", "at", "as", "if", "then", "so", "just",
	"also", "do", "from", "into", "any", "some", "i", "but", "only", "again",
)

// promptStarters are first words that are natural-language requests rather
// than mistyped commands; they are never offered as typo corrections.
var promptStarters = set(
	"help", "explain", "fix", "show", "tell", "write", "create", "add",
	"remove", "delete", "update", "summarize", "summarise", "describe",
	"refactor", "review", "list", "give", "generate", "check", "hi", "hello",
	"hey", "thanks", "thank", "ok", "okay", "yes", "no", "yeah", "nope",
	"let's", "lets", "can", "could", "would", "should", "is", "are", "do",
	"does", "why", "how", "what", "where", "when", "who", "which", "run",
	"rename", "move", "make", "find", "search", "implement", "debug", "try",
)

// precommands are words after which the next word is still in command
// position. The zsh plugin must skip the same words when it computes the
// kind of the first word.
var precommands = set(
	"sudo", "doas", "noglob", "command", "builtin", "exec", "nocorrect",
	"env", "time", "nohup", "-",
)

// precommandArgFlags lists the options of a precommand that take a separate
// argument, so that "sudo -u root rm" finds rm and not root.
var precommandArgFlags = map[string]map[string]bool{
	"sudo": set("-u", "-g", "-h", "-p", "-C", "-D", "-r", "-t", "-U", "-T", "--user", "--group", "--host", "--prompt", "--chdir"),
	"doas": set("-u", "-C"),
	"env":  set("-u", "-C", "-S", "--unset", "--chdir", "--split-string"),
	"exec": set("-a"),
}

// knownCommands are candidates for typo suggestions, most common first; the
// first candidate within the edit distance wins.
var knownCommands = []string{
	"git", "ls", "cd", "cat", "grep", "vim", "nvim", "make", "docker", "go",
	"ssh", "cp", "mv", "rm", "mkdir", "less", "tail", "head", "find", "echo",
	"sudo", "curl", "wget", "kubectl", "npm", "npx", "node", "python",
	"python3", "pip", "cargo", "nix", "tmux", "man", "which", "chmod",
	"chown", "touch", "rmdir", "ln", "pwd", "ps", "top", "htop", "kill",
	"pkill", "sed", "awk", "sort", "uniq", "wc", "cut", "tr", "xargs", "tar",
	"zip", "unzip", "gzip", "diff", "patch", "tee", "env", "export", "source",
	"alias", "history", "jobs", "exit", "clear", "systemctl", "journalctl",
	"rsync", "scp", "ping", "ip", "du", "df", "free", "mount", "umount",
	"date", "sleep", "watch", "stat", "file", "open", "code", "gh", "jq",
	"yq", "rg", "fd", "fzf", "bat", "eza", "exa", "apt", "brew", "pacman",
	"dnf", "yarn", "pnpm", "bun", "deno", "rustc", "gcc", "clang", "java",
	"helm", "terraform", "ansible", "podman", "zsh", "bash", "fish",
	"nano", "emacs", "printf", "test", "true", "false", "basename",
	"dirname", "realpath", "readlink", "chgrp", "whoami", "uname", "shutdown",
	"reboot", "dd", "mkfs", "truncate", "claude", "codex", "gemini",
	"opencode", "ecdy",
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
