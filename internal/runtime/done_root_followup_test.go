package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/items"
)

// followUpGit is the integrated git for a Done root's follow-up work: new shas on both repos.
var followUpGit = []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "4a0d1bc0000"}, {Repo: "docs", Branch: "swarm/chore-1", SHA: "bbb2220000"}}

// doneRootWithFollowUpTask finishes the fixture's root (Done), then adds a follow-up task and finishes it.
func doneRootWithFollowUpTask(t *testing.T) (s *Store, ses, key string) {
	t.Helper()
	s, ses, key = customFinishFixture(t)
	finishKept(t, s, ses)
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("setup: status = %s, want done", st)
	}
	child, err := s.Items.Create(context.Background(), items.CreateInput{Type: items.Task, ParentKey: key, Title: "Correction"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE items SET status = 'done', updated_at = ? WHERE id = ?`, db.Millis(s.Now()), child.ID)
	return s, ses, key
}

func writeIntegrated(s *Store, ses string, git []GitRef) (CheckpointResult, error) {
	return s.WriteCheckpoint(context.Background(), ses, CheckpointInput{Kind: Integrated, Summary: "follow-up integrated",
		Git: git, Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}})
}

// BUG-183: after a root's first finish the user asks for corrections. A new integrated checkpoint with new
// work reopens the root into review and opens a fresh finish request; finishing then works and returns it to Done.
func TestNewIntegratedOnDoneRootReopensFinish(t *testing.T) {
	s, ses, key := doneRootWithFollowUpTask(t)
	if _, err := writeIntegrated(s, ses, followUpGit); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.InReview {
		t.Fatalf("status after a new integrated = %s, want in_review", st)
	}
	if st := acceptState(t, s, key); st != "open" {
		t.Fatalf("finish request after a new integrated = %s, want open", st)
	}
	mustExec(t, s.DB, `UPDATE requests SET state = 'approved', responded_at = 3,
		binding_json = json_set(binding_json, '$.merge', 'custom') WHERE item_id = ? AND state = 'open'`, mustItemID(t, s, key))
	finishKept(t, s, ses)
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status after the second finish = %s, want done", st)
	}
}

// The same for a root an older binary left Done while follow-up work was done under it: the new integrated
// checkpoint itself reopens it.
func TestNewIntegratedOnStillDoneRootReopensFinish(t *testing.T) {
	s, ses, key := doneRootWithFollowUpTask(t)
	mustExec(t, s.DB, `UPDATE items SET status = 'done' WHERE id = ?`, mustItemID(t, s, key))
	if _, err := writeIntegrated(s, ses, followUpGit); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.InReview {
		t.Fatalf("status after a new integrated = %s, want in_review", st)
	}
	if st := acceptState(t, s, key); st != "open" {
		t.Fatalf("finish request after a new integrated = %s, want open", st)
	}
}

// An integrated checkpoint with the shas the root already finished with is no new work.
func TestSameShaIntegratedOnDoneRootStaysDone(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	finishKept(t, s, ses)
	same := []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "3f9c2ab0000"}, {Repo: "docs", Branch: "swarm/chore-1", SHA: "aaa1110000"}}
	if _, err := writeIntegrated(s, ses, same); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status after a same-sha integrated = %s, want done", st)
	}
	var n int
	mustQueryRow(t, s, &n, `SELECT COUNT(*) FROM requests WHERE item_id = ? AND kind = 'accept_fix' AND state = 'open'`, mustItemID(t, s, key))
	if n != 0 {
		t.Fatalf("open finish requests = %d, want 0", n)
	}
}

// Rows an old binary wrote (a Done root with an integrated checkpoint after Done) are not reopened by reconcile.
func TestReconcileDoesNotReopenHistoricalDoneRoot(t *testing.T) {
	s, ses, key := doneRootWithFollowUpTask(t)
	mustExec(t, s.DB, `UPDATE items SET status = 'done' WHERE id = ?`, mustItemID(t, s, key)) // as the old binary left it
	var orchID string
	mustQueryRow(t, s, &orchID, `SELECT agent_id FROM sessions WHERE id = ?`, ses)
	mustExec(t, s.DB, `INSERT INTO checkpoints (id, agent_id, session_id, item_id, attempt, kind, summary, git_json, verify_json, created_at)
		VALUES ('ckp_old', ?, ?, ?, 1, 'integrated', 'old binary', '[{"repo":"proj","branch":"swarm/chore-1","sha":"4a0d1bc0000"}]', '[]', ?)`,
		orchID, ses, mustItemID(t, s, key), db.Millis(s.Now())+1)
	if err := s.Items.Reconcile(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("reconcile reopened a historical Done root: status = %s", st)
	}
}

// startFollowUp readies a draft task as the user, then starts it as the daemon does once an agent on it has
// written accepted.
func startFollowUp(t *testing.T, s *Store, ses string, child items.Item) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Items.Transition(ctx, child.Key, items.Ready, items.User("board")); err != nil {
		t.Fatal(err)
	}
	var agentID string
	mustQueryRow(t, s, &agentID, `SELECT agent_id FROM sessions WHERE id = ?`, ses)
	mustExec(t, s.DB, `INSERT INTO checkpoints (id, agent_id, session_id, item_id, attempt, kind, summary, git_json, verify_json, created_at)
		VALUES (?, ?, ?, ?, 1, 'accepted', 'on it', '[]', '[]', ?)`, "ckp_acc_"+child.Key, agentID, ses, child.ID, db.Millis(s.Now()))
	if _, err := s.Items.Transition(ctx, child.Key, items.InProgress, items.Daemon()); err != nil {
		t.Fatal(err)
	}
}

func mustQueryRow(t *testing.T, s *Store, dest any, q string, args ...any) {
	t.Helper()
	if err := s.DB.QueryRowContext(context.Background(), q, args...).Scan(dest); err != nil {
		t.Fatal(err)
	}
}

// BUG-183: a follow-up task that starts under a Done root puts the root back In progress, so the board and
// the daemon agree there is work in flight. A task that is only created (a draft) does not.
func TestFollowUpTaskStartReopensDoneRoot(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	finishKept(t, s, ses)
	ctx := context.Background()
	child, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: key, Title: "Correction"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status after only creating a follow-up task = %s, want done", st)
	}
	startFollowUp(t, s, ses, child)
	if st := itemStatus(t, s, key); st != items.InProgress {
		t.Fatalf("status after a follow-up task starts = %s, want in_progress", st)
	}
}

// A follow-up task that is created and then cancelled or archived never started: the root stays Done and its
// used finish request keeps its approval.
func TestCancelledOrArchivedFollowUpLeavesDoneRootAlone(t *testing.T) {
	for _, how := range []string{"cancel", "archive"} {
		t.Run(how, func(t *testing.T) {
			s, ses, key := customFinishFixture(t)
			finishKept(t, s, ses)
			ctx := context.Background()
			child, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: key, Title: "Mistake"}, items.User("board"))
			if err != nil {
				t.Fatal(err)
			}
			if how == "cancel" {
				_, err = s.Items.Transition(ctx, child.Key, items.Cancelled, items.User("board"))
			} else {
				mustExec(t, s.DB, `UPDATE items SET archived_at = 1 WHERE id = ?`, child.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Items.Reconcile(ctx, key); err != nil {
				t.Fatal(err)
			}
			if st := itemStatus(t, s, key); st != items.Done {
				t.Fatalf("status = %s, want done", st)
			}
			if st := acceptState(t, s, key); st != "approved" {
				t.Fatalf("used finish request = %s, want approved", st)
			}
		})
	}
}

// A follow-up that started and was then cancelled leaves the root In progress; the used finish request keeps
// its approval history instead of turning stale.
func TestStartedThenCancelledFollowUpKeepsUsedApproval(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	finishKept(t, s, ses)
	ctx := context.Background()
	child, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: key, Title: "Correction"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	startFollowUp(t, s, ses, child)
	if _, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: key, Title: "Draft more"}, items.User("board")); err != nil {
		t.Fatal(err)
	}
	if st := acceptState(t, s, key); st != "approved" {
		t.Fatalf("used finish request = %s, want approved", st)
	}
}

// A Done root another root resolved stays Done when a task is added under it.
func TestFollowUpTaskKeepsResolvedRootDone(t *testing.T) {
	s, _, key := customFinishFixture(t)
	ctx := context.Background()
	other, err := s.Items.Create(ctx, items.CreateInput{Type: items.Chore, Title: "Other"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE items SET status = 'done', resolved_by_id = ? WHERE id = ?`, other.ID, mustItemID(t, s, key))
	if _, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: key, Title: "More"}, items.User("board")); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("resolved root status = %s, want done", st)
	}
}

// Rows an old binary wrote: a Done root with a live follow-up task. Reconcile leaves it Done.
func TestReconcileLeavesDoneRootWithLiveChildDone(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	finishKept(t, s, ses)
	child, err := s.Items.Create(context.Background(), items.CreateInput{Type: items.Task, ParentKey: key, Title: "Correction"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE items SET status = 'done' WHERE id = ?`, mustItemID(t, s, key))
	mustExec(t, s.DB, `UPDATE items SET status = 'in_progress' WHERE id = ?`, child.ID)
	if err := s.Items.Reconcile(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status = %s, want done", st)
	}
}

// The final-note nudge asks whether post-merge work is finished. It stays quiet while a follow-up task is live
// under the Done root, however the root got there.
func TestFinalNoteNudgeSkipsDoneRootWithLiveFollowUp(t *testing.T) {
	s, _, at, orch, _, key := doneRootOrchestrator(t)
	child, err := s.Items.Create(context.Background(), items.CreateInput{Type: items.Task, ParentKey: key, Title: "Correction"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	// What an old binary left: the root still Done, the follow-up task in flight.
	mustExec(t, s.DB, `UPDATE items SET status = 'done' WHERE id = ?`, mustItemID(t, s, key))
	mustExec(t, s.DB, `UPDATE items SET status = 'in_progress' WHERE id = ?`, child.ID)
	at.Advance(10 * time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 0 {
		t.Fatalf("live follow-up task: %d nudges, want 0", n)
	}
	// Once the follow-up is done the nudge resumes.
	mustExec(t, s.DB, `UPDATE items SET status = 'done' WHERE id = ?`, child.ID)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 1 {
		t.Fatalf("finished follow-up task: %d nudges, want 1", n)
	}
}
