// Package sessionlog records the commands of a shell session and turns them
// into the context block sent to the agent.
package sessionlog

import (
	"regexp"
	"strings"
	"unicode"
)

// Placeholder replaces every secret found by Redact.
const Placeholder = "[REDACTED]"

// Redact replaces secrets in s with Placeholder, keeping the text around
// them: the name of a variable, flag or header, quotes, the rest of a URL.
// It is a text heuristic, not a shell parser: it errs on the side of
// redacting (AGENTS.md section 2, invariant 4). Redact(Redact(s)) == Redact(s).
//
// Recognised: private key blocks; known token formats anywhere; HTTP
// authentication headers; passwords in URLs; NAME=value, --name=value,
// --name value, "name": "value" and ?name=value when a word of the name is
// secret (see secretName); a few tools' own flags (curl -u, mysql -p,
// sshpass -p, docker login -p).
func Redact(s string) string {
	s = redactPrivateKeys(s)
	s = tokenRe.ReplaceAllString(s, Placeholder)
	s = replaceGroup(s, headerRe, 1)
	s = replaceGroup(s, urlPasswordRe, 1)
	for _, re := range toolRes {
		s = replaceGroup(s, re, 1)
	}
	s = replaceGroup(s, jsonRe, 2, secretJSON)
	s = redactAssignments(s)
	s = redactFlagValues(s)
	return s
}

var (
	privateKeyBeginRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----\n?`)
	privateKeyEndRe   = regexp.MustCompile(`\n?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// redactPrivateKeys replaces the body of every PEM private key block, up to
// its END line or, for a truncated block, to the end of s.
func redactPrivateKeys(s string) string {
	var b strings.Builder
	for {
		loc := privateKeyBeginRe.FindStringIndex(s)
		if loc == nil {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:loc[1]])
		rest := s[loc[1]:]
		end := privateKeyEndRe.FindStringIndex(rest)
		switch {
		case end == nil:
			if rest != "" {
				b.WriteString(Placeholder)
			}
			return b.String()
		case end[0] > 0:
			b.WriteString(Placeholder)
		}
		b.WriteString(rest[end[0]:end[1]])
		s = rest[end[1]:]
	}
}

// tokenRe matches tokens with a recognisable prefix. The formats come from
// the providers' documentation and gitleaks' rules
// (https://github.com/gitleaks/gitleaks/blob/master/config/gitleaks.toml).
var tokenRe = regexp.MustCompile(`\b(?:` + strings.Join([]string{
	`gh[pousr]_[A-Za-z0-9]{36,}`,                                 // GitHub
	`github_pat_[A-Za-z0-9_]{22,}`,                               // GitHub fine-grained
	`glpat-[A-Za-z0-9_-]{20,}`,                                   // GitLab
	`xox[abeoprs]-[A-Za-z0-9-]{10,}`,                             // Slack
	`(?:AKIA|ASIA)[0-9A-Z]{16}\b`,                                // AWS access key id
	`sk-(?:ant|proj)-[A-Za-z0-9_-]{20,}`,                         // Anthropic, OpenAI
	`sk-[A-Za-z0-9]{32,}`,                                        // OpenAI legacy
	`AIza[0-9A-Za-z_-]{35}`,                                      // Google API
	`[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`,                       // Stripe
	`npm_[A-Za-z0-9]{36,}`,                                       // npm
	`hf_[A-Za-z0-9]{30,}`,                                        // Hugging Face
	`tskey-[a-z]+-[A-Za-z0-9-]{10,}`,                             // Tailscale
	`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`, // JWT
}, "|") + `)`)

// headerRe matches the value of an authentication header, after an optional
// quote (httpie's Name:'value') and scheme, up to a quote or the end of the line.
var headerRe = regexp.MustCompile(`(?i)\b(?:(?:proxy-)?authorization|x-[a-z0-9-]*(?:key|token|secret|auth)[a-z0-9-]*|(?:set-)?cookie)[ \t]*:[ \t]*['"]?(?:(?:bearer|basic|token|digest|negotiate)[ \t]+)?([^'"\n]+)`)

// urlPasswordRe matches the password in scheme://user:password@host.
var urlPasswordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@'"]*:([^@\s/'"]+)@`)

// toolRes match passwords given to specific tools by short flags. The part
// before the flag stays within one command of a pipeline or list.
var toolRes = []*regexp.Regexp{
	regexp.MustCompile(`\bcurl\b[^\n|;&]*?\s(?:-u|--user)(?:[ \t]+|=)[^\s:'"]*:([^\s'"]+)`),
	regexp.MustCompile(`\b(?:mysql|mariadb|mysqldump|mysqladmin|mariadb-dump)\b[^\n|;&]*?\s-p([^\s'"]+)`),
	regexp.MustCompile(`\bsshpass\b[^\n|;&]*?\s-p[ \t]*([^\s'"$-][^\s'"]*)`),
	regexp.MustCompile(`\b(?:docker|podman|nerdctl)[ \t]+login\b[^\n|;&]*?\s-p[ \t]+([^\s'"$-][^\s'"]*)`),
}

// jsonRe matches "name": "value" with a non-empty value.
var jsonRe = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_-]*)"[ \t]*:[ \t]*"((?:[^"\\\n]|\\.)+)"`)

func secretJSON(s string, m []int) bool { return secretName(s[m[2]:m[3]]) }

// replaceGroup replaces submatch group g of every match of re with
// Placeholder, if keep (when given) approves the match.
func replaceGroup(s string, re *regexp.Regexp, g int, keep ...func(string, []int) bool) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		if m[2*g] < 0 || len(keep) > 0 && !keep[0](s, m) {
			continue
		}
		b.WriteString(s[last:m[2*g]])
		b.WriteString(Placeholder)
		last = m[2*g+1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// assignRe matches NAME=, --name=, ?name= and a.name=. A match is leftmost-first, so
// it starts at the beginning of a name: MONKEY= is never read as KEY=.
var assignRe = regexp.MustCompile(`-{0,2}([A-Za-z_][A-Za-z0-9_-]*)=`)

func redactAssignments(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range assignRe.FindAllStringSubmatchIndex(s, -1) {
		if m[1] < last || !secretName(s[m[2]:m[3]]) {
			continue
		}
		start, end, ok := valueSpan(s, m[1])
		if !ok {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(Placeholder)
		last = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// flagRe matches a long flag followed by blanks: --name value.
var flagRe = regexp.MustCompile(`(?:^|\s)--([A-Za-z][A-Za-z0-9_-]*)[ \t]+`)

func redactFlagValues(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range flagRe.FindAllStringSubmatchIndex(s, -1) {
		if m[1] < last || !secretName(s[m[2]:m[3]]) {
			continue
		}
		if i := m[1]; i < len(s) && s[i] == '-' {
			continue
		}
		start, end, ok := valueSpan(s, m[1])
		if !ok {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(Placeholder)
		last = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// valueSpan returns the part of the value starting at s[i] to replace: the
// inside of a quoted value (to the closing quote or the end of the line), or
// an unquoted word. ok is false for an empty value and for one that starts
// with a parameter expansion or command substitution, which is not itself
// a secret.
func valueSpan(s string, i int) (start, end int, ok bool) {
	if i >= len(s) {
		return 0, 0, false
	}
	switch q := s[i]; q {
	case '\'', '"':
		start = i + 1
		end = start
		for end < len(s) && s[end] != q && s[end] != '\n' {
			if q == '"' && s[end] == '\\' && end+1 < len(s) && s[end+1] != '\n' {
				end++
			}
			end++
		}
		if q == '"' && strings.HasPrefix(s[start:], "$") {
			return 0, 0, false
		}
	default:
		if q == '$' {
			return 0, 0, false
		}
		start = i
		end = i + strings.IndexFunc(s[i:], func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune(";&|)<>`'\"", r)
		})
		if end < i {
			end = len(s)
		}
	}
	return start, end, end > start
}

// secretWords are the words of a name that mark its value as secret.
var secretWords = map[string]bool{
	"key": true, "apikey": true, "token": true, "secret": true,
	"password": true, "passwd": true, "pass": true, "passphrase": true, "pwd": true,
	"credential": true, "credentials": true, "pat": true, "cookie": true,
}

// harmlessLast are last words of a name that say the value is about the
// secret, not the secret itself: KEY_FILE, TOKEN_URL, --password-stdin.
var harmlessLast = map[string]bool{
	"file": true, "path": true, "dir": true, "stdin": true, "id": true,
	"url": true, "name": true, "type": true, "count": true, "length": true, "size": true,
}

// secretName reports whether a variable, flag or field name holds a secret:
// one of its words (split at _, - and camelCase) is in secretWords and its
// last word is not in harmlessLast. PWD alone is the working directory.
func secretName(name string) bool {
	words := nameWords(name)
	if len(words) == 0 || harmlessLast[words[len(words)-1]] {
		return false
	}
	if len(words) == 1 && words[0] == "pwd" {
		return false
	}
	for _, w := range words {
		if secretWords[w] {
			return true
		}
	}
	return false
}

// nameWords splits a name into lower-case words at '_', '-' and lower→upper
// case changes: "apiKey" → [api key], "AWS_SECRET" → [aws secret].
func nameWords(name string) []string {
	var words []string
	var cur []rune
	prevLower := false
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range name {
		switch {
		case r == '_' || r == '-':
			flush()
			prevLower = false
			continue
		case unicode.IsUpper(r) && prevLower:
			flush()
		}
		cur = append(cur, r)
		prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
	}
	flush()
	return words
}
