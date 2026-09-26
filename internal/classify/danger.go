package classify

import (
	"path"
	"slices"
	"strings"
)

// isDangerous reports whether any simple command in the line is destructive
// (AGENTS.md section 4, rule 5).
func isDangerous(toks []token, sh shape) bool {
	for n, ci := range sh.cmdWords {
		end := sh.segEnd[n]
		var args []string
		for _, ai := range sh.args {
			if ai > ci && ai < end {
				args = append(args, unquote(toks[ai].text))
			}
		}
		if dangerousCommand(cmdName(toks[ci]), args) {
			return true
		}
	}
	return false
}

func dangerousCommand(name string, args []string) bool {
	switch name {
	case "rm", "dd", "kill", "pkill", "killall", "shutdown", "reboot",
		"poweroff", "halt", "truncate", "shred", "wipefs":
		return true
	case "chmod", "chown", "chgrp":
		return hasFlag(args, 'R', "--recursive")
	case "find":
		for i, a := range args {
			if a == "-delete" || (a == "-exec" || a == "-execdir" || a == "-ok") && i+1 < len(args) && args[i+1] == "rm" {
				return true
			}
		}
		return false
	case "git":
		return dangerousGit(args)
	case "xargs":
		// xargs runs its first non-option argument as a command.
		for i := 0; i < len(args); i++ {
			a := args[i]
			if !strings.HasPrefix(a, "-") {
				return dangerousCommand(path.Base(a), args[i+1:])
			}
			if xargsArgFlags[a] {
				i++
			}
		}
		return false
	}
	return strings.HasPrefix(name, "mkfs")
}

// xargsArgFlags are xargs options that take a separate value.
var xargsArgFlags = set("-I", "-n", "-P", "-L", "-s", "-d", "-E", "-a")

func dangerousGit(args []string) bool {
	// Skip global options; -C and -c take a value.
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-C" || args[i] == "-c" {
			i++
		}
		i++
	}
	if i >= len(args) {
		return false
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "reset":
		return slices.Contains(rest, "--hard")
	case "clean":
		return true
	case "push":
		for _, a := range rest {
			if strings.HasPrefix(a, "--force") || strings.HasPrefix(a, "+") {
				return true
			}
		}
		return hasFlag(rest, 'f', "--force")
	}
	return false
}

// hasFlag reports whether args contain the long flag or the short flag,
// alone or in a cluster ("-Rf").
func hasFlag(args []string, short byte, long string) bool {
	for _, a := range args {
		if a == long {
			return true
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], short) >= 0 {
			return true
		}
	}
	return false
}
