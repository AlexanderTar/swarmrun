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
