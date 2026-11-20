package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/items"
	"github.com/AlexanderTar/swarmrun/internal/workflow"
)

// overCapVerify is a verify list whose rendered kickoff is past items.MaxBrief while the brief alone
// is under it: Verify is never collapsed by RenderBrief, so no cascade can save the step.
func overCapVerify() []string {
	return []string{strings.Repeat("go test ./... ", 450)}
}

// BUG-167: a plan whose task kickoff can't fit the brief cap is refused when registered, naming the
// task and its size, not at swarm_workflow start after the user approved it.
func TestRegisterPlanRefusesATaskWhoseKickoffExceedsTheCap(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedRepo(t, s, "chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Long brief", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	long := `"verify":["` + overCapVerify()[0] + `"]`
	body := strings.Replace(planBody, `"verify":["go test ./..."],"solo":"focused"},
               {"ref":"t2"`, long+`,"solo":"focused"},
               {"ref":"t2"`, 1)
	if body == planBody {
		t.Fatal("test plan not rewritten")
	}
	_, err = s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, body), "")
	if err == nil || !strings.Contains(err.Error(), "Session cookie") || !strings.Contains(err.Error(), items.ErrBriefTooLong) {
		t.Fatalf("err = %v, want a refusal naming the task with %q", err, items.ErrBriefTooLong)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE kind = 'plan'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plan artifacts = %d, %v; want none", n, err)
	}
	// A plan whose tasks fit still registers.
	if _, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, planBody), ""); err != nil {
		t.Fatalf("fitting plan refused: %v", err)
	}
}

// BUG-167: creating or updating a task whose kickoff can't fit is refused when written.
func TestTaskCreateAndUpdateRefuseAKickoffOverTheCap(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key, Title: "Story"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	in := items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "Long", Brief: "b",
		Acceptance: []string{"a"}, Steps: []string{"x"}, Verify: overCapVerify(), Solo: "s", Workflow: &workflow.Spec{Template: "tdd-reviewed"}}
	if _, err := s.Items.Create(ctx, in, items.Daemon()); err == nil || !strings.Contains(err.Error(), items.ErrBriefTooLong) {
		t.Fatalf("create err = %v, want %q", err, items.ErrBriefTooLong)
	}
	in.Verify = []string{"go test ./..."}
	task, err := s.Items.Create(ctx, in, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	long := overCapVerify()
	_, err = s.Items.Update(ctx, task.Key, items.Patch{Revision: task.Revision, Verify: &long}, items.Daemon())
	if err == nil || !strings.Contains(err.Error(), items.ErrBriefTooLong) {
		t.Fatalf("update err = %v, want %q", err, items.ErrBriefTooLong)
	}
}

// A plan task has no key yet, so registration must measure its kickoff with a realistic stand-in key,
// not one built from its title: RenderBrief prints the key in the header and once per collapsed unit,
// so a long title would refuse plans that fit once the task has a real key.
func TestPlanCheckMeasuresTheKickoffWithAKeyNotTheTitle(t *testing.T) {
	title := strings.Repeat("A long task title ", 5)
	units := make([]TreeUnit, 8)
	for i := range units {
		units[i] = TreeUnit{Title: "Unit", Steps: []string{strings.Repeat("s", 300)}}
	}
	node := func(brief string) TreeNode {
		return TreeNode{Type: "task", Title: title, Brief: brief, Acceptance: []string{"a"}, Units: units,
			Workflow: &workflow.Spec{Template: "tdd-reviewed"}, Verify: []string{"go test ./..."}}
	}
	asItem := func(n TreeNode) items.Item {
		us := make([]items.Unit, len(n.Units))
		for i, u := range n.Units {
			us[i] = items.Unit{Title: u.Title, Steps: u.Steps}
		}
		wf, err := resolvedTreeWorkflow(n)
		if err != nil {
			t.Fatal(err)
		}
		return items.Item{Key: "TASK-99999", Type: items.Task, Title: n.Title, Brief: n.Brief, Acceptance: n.Acceptance,
			Units: us, Verify: n.Verify, Workflow: wf}
	}
	// The longest brief the task-create check accepts for a real key.
	lo, hi := 0, 8000
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if CheckItemBriefs(asItem(node(strings.Repeat("b", mid)))) == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 || lo == 8000 {
		t.Fatalf("calibration failed: longest fitting brief = %d", lo)
	}
	n := node(strings.Repeat("b", lo))
	if err := CheckItemBriefs(asItem(n)); err != nil {
		t.Fatalf("task-create check refused its own calibrated brief: %v", err)
	}
	if err := checkTreeBriefs(Tree{Children: []TreeNode{n}}); err != nil {
		t.Fatalf("plan registration refused a task the create check accepts (brief %d chars): %v", lo, err)
	}
}

// The title is rendered in the kickoff header, so a title-only update that pushes a near-cap task over
// the cap is refused when written.
func TestTaskUpdateRefusesATitleThatPushesTheKickoffOverTheCap(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key, Title: "Story"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(verify string) items.CreateInput {
		return items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "T", Brief: "b",
			Acceptance: []string{"a"}, Steps: []string{"x"}, Verify: []string{verify}, Solo: "s",
			Workflow: &workflow.Spec{Template: "tdd-reviewed"}}
	}
	resolved, err := workflow.Resolve(workflow.Spec{Template: "tdd-reviewed"}, false)
	if err != nil {
		t.Fatal(err)
	}
	// The longest verify (never collapsed by RenderBrief) that still fits with the short title.
	lo, hi := 0, 8000
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if CheckItemBriefs(items.Item{Key: "TASK-99999", Type: items.Task, Title: "T", Brief: "b", Acceptance: []string{"a"},
			Steps: []string{"x"}, Verify: []string{strings.Repeat("v", mid)}, Workflow: &resolved}) == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	task, err := s.Items.Create(ctx, mk(strings.Repeat("v", lo-20)), items.Daemon())
	if err != nil {
		t.Fatalf("near-cap task (verify %d) refused: %v", lo-20, err)
	}
	long := strings.Repeat("A much longer title ", 8)
	if _, err := s.Items.Update(ctx, task.Key, items.Patch{Revision: task.Revision, Title: &long}, items.Daemon()); err == nil ||
		!strings.Contains(err.Error(), items.ErrBriefTooLong) {
		t.Fatalf("title update err = %v, want %q", err, items.ErrBriefTooLong)
	}
}
