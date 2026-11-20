package hook

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// isClaudeCommand reports whether a shell command line would start the
// agent CLI (claude) as a command. It judges only words in command position:
// the name inside a quoted pattern, a file name or an echo string is data.
//
// It is a small quote-aware shell scanner, not a full parser. It follows the
// forms that start a command (separators, subshells, $( ), backticks, <( ),
// env-style assignments, exec/nohup/sudo/env wrappers) and recurses into code
// strings (sh -c, bash -c, eval, and command substitutions). Anything it
// cannot read it treats as a block, so the guard fails closed.
//
// Known limits, shared with the regex it replaces: a command name produced by
// a variable or glob (`$c`, `cl*de`), a script fed on stdin by a pipe
// (`echo claude | sh`), and wrappers it does not know.
func isClaudeCommand(cmd string) bool {
	return newShellScan(cmd, 0).blocked()
}

// maxShellDepth bounds nested code strings; deeper input is blocked.
const maxShellDepth = 8

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\[[^\]]*\])?\+?=`)

// shellMarker stands in for text the scanner cannot know (an expansion) and
// shellRedir marks a redirection operator, whose next word is a file name.
const (
	shellMarker = "\x00"
	shellRedir  = "\x01"
)

var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"ash": true, "csh": true, "tcsh": true, "fish": true,
}

// reservedWords start a new command position without being the command.
var reservedWords = map[string]bool{
	"{": true, "}": true, "!": true, "if": true, "then": true, "else": true,
	"elif": true, "do": true, "while": true, "until": true,
}

// wrapperArgOpts lists, per wrapper that runs the rest of its arguments as a
// command, the options that take a separate argument word, so
// `sudo -u root claude` finds claude after `root`. A wrapper never launders
// the agent: whatever follows its options is judged as a command.
var wrapperArgOpts = map[string]map[string]bool{
	"exec":       {"-a": true},
	"nohup":      {},
	"builtin":    {},
	"command":    {},
	"setsid":     {},
	"time":       {"-f": true, "-o": true, "--format": true, "--output": true},
	"nice":       {"-n": true, "--adjustment": true},
	"ionice":     {"-c": true, "-n": true, "-p": true, "-P": true, "-u": true},
	"stdbuf":     {"-i": true, "-o": true, "-e": true},
	"caffeinate": {"-t": true, "-w": true},
	"doas":       {"-u": true, "-C": true},
	"timeout":    {"-s": true, "-k": true, "--signal": true, "--kill-after": true},
	"xargs": {
		"-a": true, "-d": true, "-E": true, "-I": true, "-L": true, "-n": true,
		"-P": true, "-s": true, "--arg-file": true, "--delimiter": true,
		"--max-args": true, "--max-procs": true, "--max-lines": true,
		"--max-chars": true,
	},
	"sudo": {
		"-u": true, "-g": true, "-h": true, "-p": true, "-C": true, "-D": true,
		"-R": true, "-r": true, "-t": true, "-T": true, "-U": true,
		"--user": true, "--group": true, "--host": true, "--prompt": true,
		"--chdir": true, "--chroot": true, "--role": true, "--type": true,
		"--other-user": true, "--close-from": true, "--command-timeout": true,
	},
	"env": {
		"-u": true, "-C": true, "--unset": true, "--chdir": true,
	},
}

// wrapperPositionals counts the plain arguments a wrapper takes before the
// command (timeout DURATION command).
var wrapperPositionals = map[string]int{"timeout": 1}

type shellScan struct {
	s         string
	pos       int
	depth     int
	shellSeen bool // a shell interpreter ran in command position
}

func newShellScan(s string, depth int) *shellScan {
	return &shellScan{s: s, depth: depth}
}

func (p *shellScan) blocked() bool {
	return p.list(false)
}

// list scans commands until the end of input, or in sub mode the `)` that
// closes a $( ) or <( ). It reports whether any command starts the agent.
func (p *shellScan) list(sub bool) bool {
	if p.depth > maxShellDepth {
		return true
	}
	var words []string
	var cur strings.Builder
	inWord := false
	parens := 0
	var heredocs []heredoc

	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	end := func() bool {
		flush()
		hit := p.checkWords(words)
		words = nil
		return hit
	}

	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == '\'':
			p.pos++
			i := strings.IndexByte(p.s[p.pos:], '\'')
			if i < 0 {
				i = len(p.s) - p.pos
			}
			cur.WriteString(p.s[p.pos : p.pos+i])
			p.pos += i + 1
			inWord = true
		case c == '"':
			p.pos++
			hit, text := p.dquote()
			if hit {
				return true
			}
			cur.WriteString(text)
			inWord = true
		case c == '$' && p.peek(1) == '\'':
			p.pos += 2
			cur.WriteString(p.ansiC())
			inWord = true
		case c == '$' && p.peek(1) == '"':
			p.pos++ // $"..." is a locale-translated double-quoted string
		case c == '$' && p.peek(1) == '(':
			p.pos += 2
			if p.sub() {
				return true
			}
			cur.WriteString(shellMarker)
			inWord = true
		case (c == '<' || c == '>') && p.peek(1) == '(':
			p.pos += 2
			if p.sub() {
				return true
			}
			cur.WriteString(shellMarker)
			inWord = true
		case c == '`':
			p.pos++
			if p.backtick() {
				return true
			}
			cur.WriteString(shellMarker)
			inWord = true
		case c == '\\':
			switch {
			case p.peek(1) == '\n':
				p.pos += 2
			case p.pos+1 < len(p.s):
				cur.WriteByte(p.s[p.pos+1])
				p.pos += 2
				inWord = true
			default:
				p.pos++
			}
		case c == '#' && !inWord:
			for p.pos < len(p.s) && p.s[p.pos] != '\n' {
				p.pos++
			}
		case c == '\n':
			p.pos++
			if end() {
				return true
			}
			for _, h := range heredocs {
				body := p.heredocBody(h)
				if p.shellSeen && newShellScan(body, p.depth+1).blocked() {
					return true
				}
			}
			heredocs = nil
		case c == ';' || c == '&' || c == '|':
			p.pos++
			if end() {
				return true
			}
		case c == '(':
			p.pos++
			parens++
			if end() {
				return true
			}
		case c == ')':
			p.pos++
			if end() {
				return true
			}
			if parens > 0 {
				parens--
			} else if sub {
				return false
			}
		case c == '<' || c == '>':
			// A redirection: an all-digit word before it is a file
			// descriptor, and the word after it is a file name.
			if inWord && isDigits(cur.String()) {
				cur.Reset()
				inWord = false
			}
			flush()
			if c == '<' && p.peek(1) == '<' && p.peek(2) != '<' {
				p.pos += 2
				if p.peek(0) == '-' {
					p.pos++
				}
				heredocs = append(heredocs, p.heredocStart())
				words = append(words, shellRedir)
				continue
			}
			for p.pos < len(p.s) && (p.s[p.pos] == '<' || p.s[p.pos] == '>') {
				p.pos++
			}
			if p.peek(0) == '&' || p.peek(0) == '|' {
				p.pos++
			}
			words = append(words, shellRedir)
		case c == ' ' || c == '\t' || c == '\r':
			p.pos++
			flush()
		default:
			cur.WriteByte(c)
			p.pos++
			inWord = true
		}
	}
	return end()
}

// sub scans a $( ) or <( ) body (opener consumed) one level deeper.
func (p *shellScan) sub() bool {
	p.depth++
	defer func() { p.depth-- }()
	return p.list(true)
}

func (p *shellScan) peek(n int) byte {
	if p.pos+n < len(p.s) {
		return p.s[p.pos+n]
	}
	return 0
}

// dquote reads a double-quoted string (opening quote already consumed) and
// returns its decoded text. Command substitutions inside are code.
func (p *shellScan) dquote() (bool, string) {
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == '"':
			p.pos++
			return false, b.String()
		case c == '\\' && p.pos+1 < len(p.s):
			n := p.s[p.pos+1]
			if n == '"' || n == '\\' || n == '$' || n == '`' {
				b.WriteByte(n)
			} else if n != '\n' {
				b.WriteByte(c)
				b.WriteByte(n)
			}
			p.pos += 2
		case c == '$' && p.peek(1) == '(':
			p.pos += 2
			if p.sub() {
				return true, ""
			}
			b.WriteString(shellMarker)
		case c == '`':
			p.pos++
			if p.backtick() {
				return true, ""
			}
			b.WriteString(shellMarker)
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return false, b.String()
}

// backtick scans a backtick command substitution (opening tick consumed).
func (p *shellScan) backtick() bool {
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '`' {
			p.pos++
			break
		}
		if c == '\\' && p.pos+1 < len(p.s) {
			n := p.s[p.pos+1]
			if n == '`' || n == '\\' || n == '$' {
				b.WriteByte(n)
				p.pos += 2
				continue
			}
		}
		b.WriteByte(c)
		p.pos++
	}
	return newShellScan(b.String(), p.depth+1).blocked()
}

// ansiC decodes a $'...' string (opening $' consumed): \xHH, octal and the
// single-character escapes, enough that $'\x63laude' reads as the name.
func (p *shellScan) ansiC() string {
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '\'' {
			p.pos++
			break
		}
		if c != '\\' || p.pos+1 >= len(p.s) {
			b.WriteByte(c)
			p.pos++
			continue
		}
		n := p.s[p.pos+1]
		p.pos += 2
		switch {
		case n == 'x' || n == 'u' || n == 'U':
			digits := map[byte]int{'x': 2, 'u': 4, 'U': 8}[n]
			j := p.pos
			for j < len(p.s) && j-p.pos < digits && isHex(p.s[j]) {
				j++
			}
			if v, err := strconv.ParseUint(p.s[p.pos:j], 16, 32); err == nil {
				b.WriteRune(rune(v))
			}
			p.pos = j
		case n >= '0' && n <= '7':
			j := p.pos
			for j < len(p.s) && j-p.pos < 2 && p.s[j] >= '0' && p.s[j] <= '7' {
				j++
			}
			v, _ := strconv.ParseUint(p.s[p.pos-1:j], 8, 32)
			b.WriteByte(byte(v))
			p.pos = j
		case n == 'n':
			b.WriteByte('\n')
		case n == 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(n)
		}
	}
	return b.String()
}

type heredoc struct {
	delim string
	tabs  bool
}

// heredocStart reads the delimiter word after `<<` (quotes removed).
func (p *shellScan) heredocStart() heredoc {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == ')' || c == '<' || c == '>' {
			break
		}
		if c == '\'' || c == '"' || c == '\\' {
			p.pos++
			continue
		}
		b.WriteByte(c)
		p.pos++
	}
	return heredoc{delim: b.String()}
}

// heredocBody consumes lines up to the delimiter line and returns them.
func (p *shellScan) heredocBody(h heredoc) string {
	start := p.pos
	for p.pos < len(p.s) {
		nl := strings.IndexByte(p.s[p.pos:], '\n')
		line := p.s[p.pos:]
		next := len(p.s)
		if nl >= 0 {
			line = p.s[p.pos : p.pos+nl]
			next = p.pos + nl + 1
		}
		if strings.TrimLeft(line, "\t") == h.delim {
			body := p.s[start:p.pos]
			p.pos = next
			return body
		}
		p.pos = next
	}
	return p.s[start:]
}

// checkWords judges one simple command's decoded words.
func (p *shellScan) checkWords(raw []string) bool {
	words := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == shellRedir {
			i++ // skip the redirect target
			continue
		}
		words = append(words, raw[i])
	}
	return p.checkCommand(words)
}

func (p *shellScan) checkCommand(words []string) bool {
	for i := 0; i < len(words); {
		w := words[i]
		if assignRe.MatchString(w) || reservedWords[w] {
			i++
			continue
		}
		base := path.Base(w)
		if base == "claude" {
			return true
		}
		if opts, ok := wrapperArgOpts[base]; ok {
			if base == "command" && hasLookupFlag(words[i+1:]) {
				return false // command -v/-V only looks the name up
			}
			if base == "env" {
				if script, ok := envSplitString(words[i+1:]); ok {
					return newShellScan(script, p.depth+1).blocked()
				}
			}
			i = skipWrapperOptions(words, i+1, opts, base == "env")
			for n := wrapperPositionals[base]; n > 0 && i < len(words); n-- {
				i++
			}
			continue
		}
		if base == "find" {
			return p.checkFindExec(words[i+1:])
		}
		if base == "su" {
			return p.checkSuCommand(words[i+1:])
		}
		if shellNames[base] {
			p.shellSeen = true
			return p.checkShellArgs(words[i+1:])
		}
		if base == "eval" {
			p.shellSeen = true
			return newShellScan(strings.Join(words[i+1:], " "), p.depth+1).blocked()
		}
		return false
	}
	return false
}

// hasLookupFlag reports a leading -v or -V of `command`, which prints where a
// name resolves instead of running it.
func hasLookupFlag(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return false
		}
		if a == "-v" || a == "-V" {
			return true
		}
	}
	return false
}

// envSplitString finds the string given to env -S / --split-string, which env
// splits into a command line and runs.
func envSplitString(args []string) (string, bool) {
	for i, a := range args {
		switch {
		case a == "-S" || a == "--split-string":
			if i+1 < len(args) {
				return args[i+1], true
			}
		case strings.HasPrefix(a, "--split-string="):
			return strings.TrimPrefix(a, "--split-string="), true
		case strings.HasPrefix(a, "-S"):
			return a[2:], true
		case !strings.HasPrefix(a, "-") && !assignRe.MatchString(a):
			return "", false
		}
	}
	return "", false
}

// checkFindExec judges the command run by find -exec/-execdir/-ok/-okdir,
// which ends at a lone ; or +. Other find arguments are data.
func (p *shellScan) checkFindExec(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(args) && args[j] != ";" && args[j] != "+" {
				j++
			}
			if p.checkCommand(args[i+1 : j]) {
				return true
			}
			i = j
		}
	}
	return false
}

// checkSuCommand judges the string given to su -c / --command.
func (p *shellScan) checkSuCommand(args []string) bool {
	for i, a := range args {
		switch {
		case (a == "-c" || a == "--command") && i+1 < len(args):
			return newShellScan(args[i+1], p.depth+1).blocked()
		case strings.HasPrefix(a, "--command="):
			return newShellScan(strings.TrimPrefix(a, "--command="), p.depth+1).blocked()
		}
	}
	return false
}

// skipWrapperOptions returns the index of the wrapped command, skipping the
// wrapper's options, their arguments, and (for env) NAME=value assignments.
func skipWrapperOptions(words []string, i int, argOpts map[string]bool, assigns bool) int {
	for i < len(words) {
		w := words[i]
		switch {
		case w == "--":
			return i + 1
		case strings.HasPrefix(w, "-") && w != "-":
			i++
			if argOpts[w] {
				i++
			}
		case assigns && assignRe.MatchString(w):
			i++
		default:
			return i
		}
	}
	return i
}

// checkShellArgs checks the script string of `sh -c <script>`. A script
// file argument is data; a shell reading stdin is covered by heredoc scanning.
func (p *shellScan) checkShellArgs(args []string) bool {
	cflag := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if cflag && i+1 < len(args) {
				return newShellScan(args[i+1], p.depth+1).blocked()
			}
			return false
		case a == "-o" || a == "+o" || a == "-O" || a == "+O" || a == "--rcfile" || a == "--init-file":
			i++
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			if strings.Contains(a, "c") {
				cflag = true
			}
		default:
			if cflag {
				return newShellScan(a, p.depth+1).blocked()
			}
			return false
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
