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
// strings (sh -c, bash -c, eval, and command substitutions). It blocks only
// what it recognises as starting the agent, and only runaway nesting beyond
// maxShellDepth is blocked unread; it does not fail closed on input it cannot
// read.
//
// Known limits, shared with the regex it replaces: a command name that an
// expansion produces (a variable's value, `cl*de`, `{claude,}`: an expansion
// only counts as empty before a literal name), a name in another letter case
// (`Claude`, which runs on a case-insensitive file system such as macOS), the
// body of a heredoc to anything but a shell, and wrappers or builtins that run
// a string it does not know. It does read the strings of sh -c, su -c, env -S,
// eval and trap, and the text earlier stages of a pipeline print (a command's
// arguments, `\n` read as a line break) when a later stage is a shell reading
// stdin (no -c string and no script file, or -s): `echo claude | sh`. Text
// that reaches a shell through anything but a pipe stage's arguments (a
// `cat file | sh`, a `curl ... | sh`) is not known.
func isClaudeCommand(cmd string) bool {
	return newShellScan(cmd, 0).blocked()
}

// maxShellDepth bounds nested code strings; deeper input is blocked.
const maxShellDepth = 8

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\[[^\]]*\])?\+?=`)

// leadingExpansion matches the markers at the start of a word: the scanner
// writes one for every expansion ($(...), `...`, $name, ${...}) before the
// word is decoded, so quoting around a name cannot hide or fake one.
var leadingExpansion = regexp.MustCompile(`^\x00+`)

// shellMarker stands in for text the scanner cannot know (an expansion) and
// shellRedir marks a redirection operator, whose next word is a file name.
const (
	shellMarker = "\x00"
	shellRedir  = "\x01"
	shellHere   = "\x02" // a here-string (<<<); its word is code for a shell
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

	// pipe holds the text the earlier stages of the pipeline being read print
	// (stageText); stage is the text of the command just judged.
	pipe  []string
	stage string
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
	lastPipe := false // the last separator was a pipe (a newline may continue it)

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
		case c == '$':
			ok, hit := p.expansion(false)
			if hit {
				return true
			}
			if ok {
				cur.WriteString(shellMarker)
			} else {
				cur.WriteByte(c)
				p.pos++
			}
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
			keep := lastPipe && len(words) == 0 && !inWord // `a |` continues on the next line
			if end() {
				return true
			}
			if !keep {
				p.pipe, lastPipe = nil, false
			}
			for _, h := range heredocs {
				body := p.heredocBody(h)
				if p.shellSeen && newShellScan(body, p.depth+1).blocked() {
					return true
				}
				// An unquoted delimiter lets the shell expand $( ) and
				// backticks in the body, whatever reads the heredoc.
				if !h.quoted && newShellScan(body, p.depth+1).heredocSubst() {
					return true
				}
			}
			heredocs = nil
		case c == '|' && p.peek(1) != '|':
			// A pipe (or |&): this stage's text is stdin for the next.
			p.pos++
			if p.peek(0) == '&' {
				p.pos++
			}
			if end() {
				return true
			}
			p.pipe, lastPipe = append(p.pipe, p.stage), true
		case c == ';' || c == '&' || c == '|':
			p.pos++
			if c == '|' {
				p.pos++ // ||
			}
			if end() {
				return true
			}
			p.pipe, lastPipe = nil, false
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
			if c == '<' && p.peek(1) == '<' && p.peek(2) == '<' {
				p.pos += 3
				words = append(words, shellHere)
				continue
			}
			if c == '<' && p.peek(1) == '<' {
				p.pos += 2
				tabs := p.peek(0) == '-'
				if tabs {
					p.pos++
				}
				h := p.heredocStart()
				h.tabs = tabs
				heredocs = append(heredocs, h)
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
	pipe := p.pipe
	p.pipe = nil
	hit := p.list(true)
	p.pipe = pipe
	p.depth--
	return hit
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
		case c == '$':
			ok, hit := p.expansion(true)
			if hit {
				return true, ""
			}
			if ok {
				b.WriteString(shellMarker)
			} else {
				b.WriteByte(c)
				p.pos++
			}
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

// expansion consumes the parameter expansion at p.pos ($name, ${...}, $1,
// $@ ...) and reports whether there was one. hit is set when a command
// substitution inside ${...} starts the agent.
func (p *shellScan) expansion(inDquote bool) (ok, hit bool) {
	n := p.peek(1)
	switch {
	case n == '{':
		p.pos += 2
		return true, p.braceBody(inDquote)
	case n == '_' || n >= 'a' && n <= 'z' || n >= 'A' && n <= 'Z':
		p.pos++
		for c := p.peek(0); c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'; c = p.peek(0) {
			p.pos++
		}
		return true, false
	case n != 0 && strings.IndexByte("0123456789@*#?!$-", n) >= 0:
		p.pos += 2
		return true, false
	}
	return false, false
}

// braceBody skips a ${...} body (opener consumed) to its closing brace. Code
// inside it (command substitutions) is scanned; quotes and nested ${ } are
// skipped so a quoted brace does not end it early.
func (p *shellScan) braceBody(inDquote bool) bool {
	for p.pos < len(p.s) {
		switch c := p.s[p.pos]; {
		case c == '}':
			p.pos++
			return false
		case c == '\\':
			p.pos += 2
		case c == '\'' && !inDquote:
			p.pos++
			i := strings.IndexByte(p.s[p.pos:], '\'')
			if i < 0 {
				i = len(p.s) - p.pos
			}
			p.pos += i + 1
		case c == '"' && !inDquote:
			p.pos++
			if hit, _ := p.dquote(); hit {
				return true
			}
		case c == '$' && p.peek(1) == '(':
			p.pos += 2
			if p.sub() {
				return true
			}
		case c == '$':
			ok, hit := p.expansion(inDquote)
			if hit {
				return true
			}
			if !ok { // a literal $
				p.pos++
			}
		case c == '`':
			p.pos++
			if p.backtick() {
				return true
			}
		default:
			p.pos++
		}
	}
	return false
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
	delim  string
	tabs   bool
	quoted bool // any quoting in the delimiter turns off expansion in the body
}

// heredocSubst scans a heredoc body (unquoted delimiter) for the command
// substitutions the shell runs in it: $( ) and backticks, unless escaped.
func (p *shellScan) heredocSubst() bool {
	for p.pos < len(p.s) {
		switch c := p.s[p.pos]; {
		case c == '\\':
			p.pos += 2
		case c == '$' && p.peek(1) == '(':
			p.pos += 2
			if p.sub() {
				return true
			}
		case c == '`':
			p.pos++
			if p.backtick() {
				return true
			}
		default:
			p.pos++
		}
	}
	return false
}

// heredocStart reads the delimiter word after `<<` (quotes removed).
func (p *shellScan) heredocStart() heredoc {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
	quoted := false
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == ')' || c == '<' || c == '>' {
			break
		}
		if c == '\'' || c == '"' || c == '\\' {
			quoted = true
			p.pos++
			continue
		}
		b.WriteByte(c)
		p.pos++
	}
	return heredoc{delim: b.String(), quoted: quoted}
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
		if h.tabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == h.delim {
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
	var here []string
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case shellRedir:
			i++ // skip the redirect target
		case shellHere:
			if i+1 < len(raw) {
				here = append(here, raw[i+1])
			}
			i++
		default:
			words = append(words, raw[i])
		}
	}
	p.stage = stageText(words)
	if len(here) == 0 {
		return p.checkCommand(words)
	}
	// A here-string is stdin: code when the command is a shell, data otherwise.
	prev := p.shellSeen
	p.shellSeen = false
	if p.checkCommand(words) {
		return true
	}
	isShell := p.shellSeen
	p.shellSeen = prev || isShell
	if !isShell {
		return false
	}
	for _, h := range here {
		if newShellScan(h, p.depth+1).blocked() {
			return true
		}
	}
	return false
}

// stageText is what a command writes to the next stage of a pipeline, taken
// to be its arguments past leading options (echo -e 'cmd'), space-joined.
func stageText(words []string) string {
	if len(words) == 0 {
		return ""
	}
	i := 1
	for i < len(words) && len(words[i]) > 1 && words[i][0] == '-' {
		i++
	}
	// printf and echo -e turn a literal \n into a line break.
	return strings.ReplaceAll(strings.Join(words[i:], " "), `\n`, "\n")
}

// shellReadsStdin reports whether a shell with these arguments reads its
// script from stdin: no -c string and no script file, or -s.
func shellReadsStdin(args []string) bool {
	positional := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return len(args[i+1:]) == 0
		case a == "-o" || a == "+o" || a == "-O" || a == "+O" || a == "--rcfile" || a == "--init-file":
			i++
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			if strings.Contains(a, "c") {
				return false
			}
			if strings.Contains(a, "s") {
				return true
			}
		default:
			positional = true
		}
	}
	return !positional
}

func (p *shellScan) checkCommand(words []string) bool {
	for i := 0; i < len(words); {
		w := words[i]
		if w == "coproc" {
			// coproc [NAME] command: NAME only precedes a compound command.
			if i+2 < len(words) && words[i+2] == "{" {
				i++
			}
			i++
			continue
		}
		if w == "function" {
			// function NAME [()] { body }: NAME is not a command.
			i += 2
			continue
		}
		if assignRe.MatchString(w) || reservedWords[w] {
			i++
			continue
		}
		// An expansion that can be empty ($(true), $VAR) before the name
		// vanishes at run time, so `$(true) claude` runs claude.
		if w = leadingExpansion.ReplaceAllString(w, ""); w == "" {
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
			if p.checkShellArgs(words[i+1:]) {
				return true
			}
			// A shell reading stdin runs what the earlier stages print.
			if shellReadsStdin(words[i+1:]) {
				for _, text := range p.pipe {
					if text != "" && newShellScan(text, p.depth+1).blocked() {
						return true
					}
				}
			}
			return false
		}
		if base == "eval" {
			p.shellSeen = true
			return newShellScan(strings.Join(words[i+1:], " "), p.depth+1).blocked()
		}
		if base == "trap" {
			return p.checkTrap(words[i+1:])
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

// checkTrap judges the action string of `trap [--] ACTION SIGNAL...`, which
// the shell runs as code when the signal arrives. -l and -p only list.
func (p *shellScan) checkTrap(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--":
			if i+1 < len(args) {
				return newShellScan(args[i+1], p.depth+1).blocked()
			}
			return false
		case a == "-l" || a == "-p" || a == "-lp" || a == "-pl":
			return false
		case len(a) > 1 && a[0] == '-':
		default:
			return newShellScan(a, p.depth+1).blocked()
		}
	}
	return false
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
		case strings.HasPrefix(w, "-") && (w != "-" || assigns): // env - is env -i
			i++
			if argOpts[w] || clusterTakesNext(w, argOpts) {
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

// clusterTakesNext reports whether a short-option cluster such as -iu ends in
// an option that takes an argument, so the argument is the next word. An
// argument-taking letter earlier in the cluster takes the rest of it (-uroot).
func clusterTakesNext(w string, argOpts map[string]bool) bool {
	if len(w) < 3 || w[1] == '-' {
		return false
	}
	for j := 1; j < len(w); j++ {
		if argOpts["-"+w[j:j+1]] {
			return j == len(w)-1
		}
	}
	return false
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
