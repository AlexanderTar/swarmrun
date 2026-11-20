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
