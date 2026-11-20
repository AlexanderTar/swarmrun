package runtime

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/AlexanderTar/swarmrun/internal/items"
)

// CheckItemBriefs is the items.Store.CheckBriefs hook: it renders the kickoff of every step its workflow
// can spawn, with stand-ins for what isn't known before a start (the orchestrator's name, the worktree
// lines, the review tree), and refuses with the one cap message when RenderBrief can't make one fit. It is
// the check swarm_workflow start runs in preflightBriefs, made when the text is written (plan registration,
// create, update), so an over-cap task is refused before the user approves it. It reads no database.
// Known ceiling: it models one worktree line and omits gateDirLines, so a multi-repo task can still be
// refused at start; swarm_workflow start's preflightBriefs stays the exact check.
func CheckItemBriefs(it items.Item) error { return checkItemBriefs(it, it.Key) }

// checkItemBriefs renders with it.Key and names label in the refusal. A plan task has no key yet: its
// caller passes a stand-in key and the task's title as the label, so a long title can't inflate the
// measured kickoff (RenderBrief prints the key in the header and once per collapsed unit).
func checkItemBriefs(it items.Item, label string) error {
	if it.Workflow == nil {
		return nil
	}
	for _, step := range it.Workflow.EffectiveSteps() {
		for _, role := range stepRoles(step) {
			brief := BriefForStep(it, *it.Workflow, step.ID, 1, 0, []string{"docs/design/artifact.md"})
			if err := setBriefIdentity(&brief, it, role, "EPIC-1000", standInOrchestrator); err != nil {
				return err
			}
			wt := BriefWorktree{Repo: "swarmrun", Path: standInWorktreePath, Branch: "epic-1000/" + strings.Repeat("s", 24),
				BaseSHA7: "abcdef0", Mode: "rw"}
			if step.Run == "" {
				wt.Path, wt.Branch, wt.Mode, wt.HeadSHA7 = standInWorktreePath+"-9", "", "ro", "abcdef1"
			}
			brief.Worktrees = []BriefWorktree{wt}
			if _, err := RenderBrief(brief); err != nil {
				full := utf8.RuneCountInString(renderBriefOnce(brief, false, false))
				return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("%s: %s (the %s kickoff is %d characters)",
					label, err.Error(), role, full)}
			}
		}
	}
	return nil
}

const (
	standInTaskKey      = "TASK-99999"
	standInOrchestrator = "a-typical-orchestrator-agent-name-orchestrator"
	standInWorktreePath = "/Users/someone/.swarm/worktrees/swarmrun--a-typical-worktree-name"
)

// checkTreeBriefs is CheckItemBriefs for every task of a plan's swarm-tree, run when the plan is
// registered. A task has no key yet: it is measured with a stand-in key and named by its title.
func checkTreeBriefs(t Tree) error {
	for _, n := range t.Tasks() {
		wf, err := resolvedTreeWorkflow(n)
		if err != nil {
			return fmt.Errorf("tree_invalid: task %q: %v", n.Title, err)
		}
		units := make([]items.Unit, len(n.Units))
		for i, u := range n.Units {
			units[i] = items.Unit{Title: u.Title, Steps: u.Steps}
		}
		if err := checkItemBriefs(items.Item{Key: standInTaskKey, Type: items.Task, Title: n.Title,
			Brief: n.Brief, Acceptance: n.Acceptance, Steps: n.Steps, Units: units, Verify: n.Verify, Workflow: wf},
			fmt.Sprintf("Task %q", n.Title)); err != nil {
			return fmt.Errorf("tree_invalid: %v", err)
		}
	}
	return nil
}
