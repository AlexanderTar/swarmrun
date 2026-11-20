package hook

import "testing"

// Data-only mentions: the agent CLI's name appears as an argument, a quoted
// pattern or a file name, never as the command being run. None may be blocked.
func TestIsClaudeCommandAllowsDataMentions(t *testing.T) {
	for _, s := range []string{
		// BUG-186: the | inside the quoted regex is not a pipe.
		`grep -i -E 'reminder|chatgpt|apple|claude|tax' *.svg`,
		// BUG-187: the quoted text starts with the agent's name.
		`grep -n "claude mcp add" file`,
		`grep -n 'claude mcp add' docs/setup.md`,
		`grep -rn "nested\|claude CLI\|isClaude" internal`,
		`rg "foo|claude" .`,
		`echo "run: claude -p hi"`,
		`echo 'a; claude -p'`,
		`echo "x && claude"`,
		`git commit -m "fix; claude -p handling"`,
		`git commit -m 'stop | claude'`,
		`sed -n '1p;claude;' file`,
		`cat claude.log`,
		`ls ~/.claude/projects`,
		`ls -la /Users/me/.claude/settings.json`,
		`diff claude.md AGENTS.md`,
		`awk '/claude/ {print}' notes.txt`,
		`printf '%s\n' claude`,
		"echo 'line one\nclaude -p two'",
		`which claude`,
		`type claude`,
		`command -v claude`,
		`bash script.sh claude`,
		`sh -c 'echo claude'`,
		`bash -c "grep -n 'claude mcp' file"`,
		`eval "echo claude"`,
		`echo $(echo claude)`,
		"echo `date` claude",
		`cat <<'EOF'
claude -p this is documentation
EOF`,
		`git commit -m "$(cat <<'EOF'
docs: don't start claude -p from a hook (it's (not) allowed
EOF
)"`,
		"cat <<EOF\n\tEOF\nclaude\nEOF",
		`for f in claude codex; do echo $f; done`,
		`# claude -p in a comment`,
		`echo ok # claude`,
		`FOO=claude env`,
		`export AGENT=claude`,
		`grep -c claude ./claude.txt 2>&1`,
	} {
		if isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = true, want false (data-only mention)", s)
		}
	}
}

// Real invocations in every command position. Each must stay blocked.
func TestIsClaudeCommandBlocksRealInvocations(t *testing.T) {
	for _, s := range []string{
		"claude",
		"claude -p 'hi'",
		"  claude -p test",
		"ls; claude",
		"ls;claude",
		"ls && claude",
		"ls&&claude -p x",
		"false || claude",
		"ls | claude",
		"ls |claude",
		"ls & claude",
		"ls &\nclaude",
		"ls\nclaude -p x",
		"ls\n  claude -p x",
		"echo hi\r\nclaude",
		"(claude)",
		"( claude -p x )",
		"ls && (cd /tmp && claude)",
		"x=$(claude)",
		"echo $(claude -p x)",
		"echo $(ls; claude)",
		"echo `claude`",
		"echo `ls && claude -p x`",
		`echo "$(claude -p x)"`,
		`echo "a $(ls | claude) b"`,
		`echo "$(echo "$(claude)")"`,
		"cat <(claude)",
		"VAR=1 claude",
		"A=1 B=2 claude -p x",
		"env claude",
		"exec claude",
		"nohup claude &",
		"sudo claude",
		"X=1 exec claude",
		"/usr/local/bin/claude -p x",
		"./claude",
		"../bin/claude -p",
		`bash -c 'echo $(claude)'`,
		`eval "$(claude)"`,
		"cat <<EOF | bash\nclaude -p x\nEOF",
		"bash <<EOF\nclaude -p x\nEOF",
		"sh <<'EOF'\nls\nclaude\nEOF",
		"bash <<-EOF\n\tclaude -p x\n\tEOF\nls",
		"claude 2>&1",
		"claude >out.txt",
		"claude <in.txt",
	} {
		if !isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = false, want true (real invocation)", s)
		}
	}
}

// Forms the base regex missed (it only knew exec/nohup/sudo/env in a fixed
// order and never looked inside sh -c or eval strings), so a nested agent
// could be started through them. The rewrite must block all of them.
func TestIsClaudeCommandBlocksBaseRegexGaps(t *testing.T) {
	for _, s := range []string{
		`{ claude -p x; }`,
		`! claude`,
		`if claude; then echo y; fi`,
		`while claude; do :; done`,
		`A='a b' claude`,
		`A="a b" claude`,
		`env X=1 claude`,
		`env -i X=1 claude`,
		`env -u HOME claude`,
		`sudo -u root claude -p x`,
		`sudo env X=1 nohup claude`,
		`~/.claude/local/claude -p x`,
		`$HOME/.local/bin/claude`,
		`/usr/bin/env claude`,
		`/usr/bin/env X=1 /opt/claude/bin/claude`,
		`sh -c 'claude -p x'`,
		`sh -c "claude -p x"`,
		`bash -c "claude"`,
		`bash -c 'ls; claude'`,
		`bash -lc 'claude -p x'`,
		`bash -ec "claude"`,
		`zsh -c 'claude'`,
		`/bin/sh -c 'claude'`,
		`env X=1 bash -c 'claude'`,
		`sudo bash -c 'claude'`,
		`bash -x -c 'claude'`,
		`bash -o pipefail -c 'claude'`,
		`bash -c 'echo hi; sh -c "claude -p x"'`,
		`bash -c 'bash -c "bash -c claude"'`,
		`eval claude`,
		`eval "claude -p x"`,
		`eval 'ls; claude'`,
		`eval ls '&&' claude`,
		`FOO=1 eval claude`,
		`echo hi | bash -c 'claude'`,
		`>out.txt claude`,
		`2>/dev/null claude`,
	} {
		if !isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = false, want true (real invocation)", s)
		}
	}
}

// Quoting must not hide the command word: the shell strips the quotes before
// running it, so these all start the agent. These are blocked on the base
// regex only by accident for some forms and missed for others.
func TestIsClaudeCommandBlocksQuotedCommandWord(t *testing.T) {
	for _, s := range []string{
		`"claude" -p x`,
		`'claude' -p x`,
		`cl"aude" -p x`,
		`"cl"aude`,
		`c\laude -p x`,
		`\claude`,
		`$'claude' -p x`,
		`$'\x63laude' -p x`,
		`$'\143laude'`,
		`$"claude"`,
		`"/usr/local/bin/claude" -p x`,
		`ls; "claude"`,
		`ls |'claude'`,
		"cla\\\nude -p x",
	} {
		if !isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = false, want true (quoted command word)", s)
		}
	}
}

// Wrappers that run their argument as a command. Decision: a wrapper never
// launders the agent, so each of these is blocked, including xargs and
// find -exec, which start it with arguments read from elsewhere.
func TestIsClaudeCommandBlocksWrapperForms(t *testing.T) {
	for _, s := range []string{
		"xargs claude",
		"echo hi | xargs claude -p",
		"xargs -n1 -I{} claude -p {}",
		"xargs -0 -r env X=1 claude",
		"xargs sh -c 'claude -p x'",
		"find . -exec claude {} ;",
		`find . -name '*.md' -exec claude -p {} \;`,
		`find . -execdir claude {} +`,
		`find . -ok claude {} \;`,
		`find . -type f -exec sh -c 'claude -p x' _ {} \;`,
		"timeout 5 claude",
		"timeout -s KILL 5 claude -p x",
		"timeout --signal=KILL 5s claude",
		"timeout --kill-after 1 5 claude",
		"command claude",
		"command -p claude",
		"builtin claude",
		"nice claude",
		"nice -n 10 claude",
		"ionice -c 3 claude",
		"time claude",
		"time -p claude",
		"stdbuf -oL claude",
		"stdbuf -o L claude",
		"setsid claude",
		"setsid -f claude",
		"doas claude",
		"doas -u root claude",
		"caffeinate -i claude",
		"env -S 'claude -p x'",
		"env -S'X=1 claude'",
		"env --split-string='claude'",
		"su -c 'claude -p x'",
		"su root -c claude",
		"sudo -u root timeout 5 xargs nice claude",
	} {
		if !isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = false, want true (wrapper form)", s)
		}
	}
}

// Review round: forms the shell itself runs as code that the first rewrite let
// through. An unquoted heredoc delimiter makes the shell expand $( ) and
// backticks in the body; coproc, a here-string to a shell and `env -` all run
// the agent too.
func TestIsClaudeCommandBlocksReviewGaps(t *testing.T) {
	for _, s := range []string{
		"cat <<EOF\n$(claude -p hi)\nEOF",
		"cat <<EOF\n`claude -p hi`\nEOF",
		"cat <<EOF > out\n${x:-$(claude)}\nEOF",
		"cat <<-EOF\n\t$(claude)\n\tEOF",
		"cat <<EOF\nok \\\\$(claude)\nEOF",
		`coproc claude`,
		`coproc claude -p hi`,
		`coproc NAME { claude -p hi; }`,
		`bash <<< 'claude -p hi'`,
		`sh <<<"claude"`,
		`env X=1 bash <<< "ls; claude"`,
		`env - claude -p hi`,
		`env - X=1 claude`,
	} {
		if !isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = false, want true (real invocation)", s)
		}
	}
}

// Their data-only neighbours stay allowed: a quoted or escaped heredoc
// delimiter means no expansion, a here-string to a non-shell is input data.
func TestIsClaudeCommandAllowsReviewData(t *testing.T) {
	for _, s := range []string{
		"cat <<'EOF'\n$(claude -p hi)\nEOF",
		"cat <<\"EOF\"\n$(claude -p hi)\nEOF",
		"cat <<\\EOF\n`claude`\nEOF",
		"cat <<EOF\n\\$(claude)\nEOF",
		"cat <<EOF\ndocs mention claude and $(date)\nEOF",
		`grep claude <<< 'claude -p hi'`,
		`cat <<< "claude"`,
		`coproc grep claude`,
		`coproc NAME { grep claude; }`,
		`env - ls claude`,
	} {
		if isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = true, want false (data)", s)
		}
	}
}

// Review round 2: an expansion that can be empty before the command name does
// not hide it, a function body is code, and a short-option cluster ending in
// an option that takes an argument consumes the next word.
func TestIsClaudeCommandBlocksRound2Gaps(t *testing.T) {
	for _, s := range []string{
		`$(true) claude -p hi`,
		"`true` claude",
		`$(:)claude`,
		`x=1 $(true) claude`,
		`eval $(echo) claude`,
		"bash <<EOF\n$(echo) claude\nEOF",
		`$EMPTY claude`,
		`${EMPTY:-} claude`,
		`$EMPTY/usr/bin/claude`,
		`sudo $(true) claude`,
		`function f { claude; }; f`,
		`function f() { claude; }`,
		`function f { claude -p x; }`,
		`sudo -iu root claude`,
		`sudo -Eu root claude`,
		`env -iu X claude`,
		`sudo -u root claude`,
	} {
		if !isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = false, want true (real invocation)", s)
		}
	}
}

func TestIsClaudeCommandAllowsRound2Data(t *testing.T) {
	for _, s := range []string{
		`$(which grep) -n x claude`,
		`function claude_notes { grep claude f; }`,
		`function f { echo claude; }`,
		`sudo -iu claude ls`,
		`sudo -uroot ls claude`,
		`env -iu claude ls`,
		`$EDITOR claude.md`,
	} {
		if isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = true, want false (data)", s)
		}
	}
}

// An expansion glued to a quoted or escaped name: the quotes are gone when the
// shell runs the word, and with the variable unset it runs claude. The scanner
// must see the expansion before the text is decoded, so quoting survives.
func TestIsClaudeCommandBlocksGluedExpansions(t *testing.T) {
	for _, s := range []string{
		`"$x"claude`,
		`$x'claude'`,
		`$x"claude"`,
		`$x\claude`,
		`"${x}"claude -p hi`,
		`"${x:-$(true)}"claude`,
		`${x:-'}'}claude`,
		`$1 claude`,
		`"$@"claude`,
		`ls; "$x"/usr/bin/claude`,
		`echo ${x:-$(claude)}`,
		`echo "${x:-$(claude)}"`,
	} {
		if !isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = false, want true (expansion glued to the name)", s)
		}
	}
}

// Text that only looks like an expansion is data: a single-quoted or escaped
// $ is a literal character, so these words never reduce to the bare name.
func TestIsClaudeCommandAllowsLiteralDollar(t *testing.T) {
	for _, s := range []string{
		`'$x'claude`,
		`"\$x"claude`,
		`\$xclaude`,
		`'${x}'claude`,
		`$ claude`,
		`echo "$x"claude`,
		`ls "$x"claude`,
	} {
		if isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = true, want false (literal $ is data)", s)
		}
	}
}

// The same wrappers around an ordinary command, and read-only lookups of the
// agent's name, are not invocations.
func TestIsClaudeCommandAllowsWrapperData(t *testing.T) {
	for _, s := range []string{
		"xargs grep claude",
		"xargs -n1 echo claude",
		"find . -name claude",
		"find . -name 'claude*' -type f",
		`find . -exec grep -l claude {} \;`,
		`find . -exec ls claude {} +`,
		"timeout 5 grep claude file",
		"timeout 5 ls",
		"command -v claude",
		"command -V claude",
		"nice -n 10 ls claude",
		"time ls claude",
		"env X=claude ls",
		"env -u claude ls",
		"sudo -u claude ls",
		"stdbuf -oL cat claude.txt",
		"su -c 'echo claude'",
		"doas ls claude",
	} {
		if isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = true, want false (data/lookup)", s)
		}
	}
}
