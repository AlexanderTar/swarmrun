package hook

import (
	"regexp"
	"testing"
)

// oldClaudeCmdRe is the text match the nested-agent guard used before the
// scanner (main at 440e6225, internal/hook/handler.go:23). It is kept here only
// as a reference: the scanner must block every real invocation it blocked.
var oldClaudeCmdRe = regexp.MustCompile(`(?m)(?:^|[;&|()` + "`" + `]|\$\()\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*(?:(?:exec|nohup|sudo|env)\s+)*(?:[\w./]*\/)?claude(\s|[;&|)` + "`" + `]|$)`)

// TestIsClaudeCommandKeepsWhatTheTextMatchBlocked lists real invocations (each
// starts the agent CLI when run) in forms an allow-list scanner can miss. Every
// one the old text match blocked must still be blocked.
func TestIsClaudeCommandKeepsWhatTheTextMatchBlocked(t *testing.T) {
	real := []string{
		"claude -p hi",
		"G=(claude -p hi); \"${G[@]}\"",
		"c='claude -p hi'; $c",
		"c='claude -p hi'; eval \"$c\"",
		"c='claude -p hi'; sh -c \"$c\"",
		"read -r c <<< 'claude -p hi'; $c",
		"echo '(claude -p hi)' | sh",
		"printf 'x\\nclaude -p hi\\n' | sh",
		"printf 'x\\nclaude -p hi\\n' > s.sh; sh s.sh",
		"sh -c '\"$@\"' sh claude -p hi",
		"sh -c '$0 \"$@\"' claude -p hi",
		"$(echo claude) -p hi",
		"`echo claude` -p hi",
		"$(echo claude -p hi)",
		"source <(echo claude -p hi)",
		"bash <(echo claude -p hi)",
		"eval \"$(echo claude -p hi)\"",
		"ssh localhost 'claude -p hi'",
		"tmux new -d 'claude -p hi'",
		"at now <<< 'claude -p hi'",
		"vim -c '!claude -p hi' -c q",
		"nix-shell --run 'claude -p hi'",
		"hyperfine 'claude -p hi'",
		". /dev/stdin <<< 'claude -p hi'",
		"python3 -c 'import os; os.system(\"claude -p hi\")'",
		"python3 <<'EOF'\nimport os\nos.system(\"claude -p hi\")\nEOF",
		"perl -e 'system(\"claude -p hi\")'",
		"node -e 'require(\"child_process\").execSync(\"claude -p hi\")'",
		"osascript -e 'do shell script \"claude -p hi\"'",
		"flock /tmp/l claude -p hi",
		"watch -n1 claude -p hi",
		"parallel claude ::: hi",
		"chronic claude -p hi",
		"script -q /dev/null claude -p hi",
		"xcrun claude -p hi",
		"taskpolicy -b claude -p hi",
		"sandbox-exec -n no-network claude -p hi",
		"busybox claude -p hi",
		"$(which claude) -p hi",
		"\"$(command -v claude)\" -p hi",
		"f() { claude \"$1\"; }; f hi",
		"function f { claude; }; f",
		"coproc claude -p hi",
		"env - claude -p hi",
		"bash <<< 'claude -p hi'",
		"cat <<EOF\n$(claude -p hi)\nEOF",
		"x=$(claude -p hi)",
		"echo `claude -p hi`",
		"(claude -p hi)",
		"{ claude -p hi; }",
		"if true; then claude -p hi; fi",
		"while false; do :; done; claude -p hi",
		"! claude -p hi",
		"time claude -p hi",
		"echo hi |& claude -p hi",
		"echo hi\nclaude -p hi",
		"echo 'a\nclaude -p hi\nb' | sh",
		"echo 'a\nclaude -p hi\nb' > s.sh; bash s.sh",
		"echo 'a\nclaude -p hi\nb' >> s.sh; . s.sh",
		"echo 'a\nclaude -p hi\nb' | tee s.sh; source s.sh",
		"echo 'a\nclaude -p hi\nb' > s.sh && chmod +x s.sh && ./s.sh",
		"cat > s.sh <<EOF\na\nclaude -p hi\nEOF\nbash s.sh",
		"cat <<EOF > s.sh && chmod +x s.sh && ./s.sh\nclaude -p hi\nEOF",
		"cat <<EOF > s.sh; . s.sh\nclaude -p hi\nEOF",
		"cat <<EOF > s.sh; source s.sh\nclaude -p hi\nEOF",
	}
	for _, cmd := range real {
		if oldClaudeCmdRe.MatchString(cmd) && !isClaudeCommand(cmd) {
			t.Errorf("old text match blocked, scanner allows: %q", cmd)
		}
	}
}
