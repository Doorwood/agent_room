package store

import (
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestProjectTaskLifecycleAndAtomicFollowup(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedRoom(t, s)
	if _, err := s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}); err != nil {
		t.Fatal(err)
	}
	source := acceptedPrompt(t, s, aliceID)
	req := room.TaskRequest{Action: "convert", RequestID: fmt.Sprintf("%032x", 10), SourceSeq: int64(source.Seq), Title: "修复登录", Acceptance: "登录成功"}
	before, _ := s.LatestSeq(ctx, "team")
	task, err := s.ChangeTask(ctx, "team", alice, req)
	if err != nil || task.State != "queued" {
		t.Fatal(task, err)
	}
	after, _ := s.LatestSeq(ctx, "team")
	if before != after {
		t.Fatal("conversion changed execution history")
	}
	again, err := s.ChangeTask(ctx, "team", alice, req)
	if err != nil || again.ID != task.ID {
		t.Fatal(again, err)
	}
	complete := room.TaskRequest{Action: "complete", RequestID: fmt.Sprintf("%032x", 11), TaskID: task.ID, ExpectedRevision: task.Revision}
	if _, err = s.ChangeTask(ctx, "team", alice, complete); err == nil {
		t.Fatal("completed queued task")
	}
	if err = s.BeginDispatch(ctx, "team", aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BindRunningTurn(ctx, "team", aliceID, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeTask(ctx, "team", alice, complete); err == nil {
		t.Fatal("completed running task")
	}
	event, err := s.RecordCompletedItem(ctx, "team", room.CompletedItem{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "i-1", Payload: json.RawMessage(`{"type":"agentMessage","text":"已修复，等待验收"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishTurn(ctx, "team", room.FinishTurnInput{TurnID: "turn-1", State: room.RequestCompleted}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Tasks(ctx, "team", room.TaskRequest{Action: "get", TaskID: task.ID})
	if err != nil || got.Task.State != "review" || len(got.Task.Runs) != 1 || len(got.Task.Runs[0].Answers) != 1 || got.Task.Runs[0].Answers[0].Time == "" {
		t.Fatal(got, err)
	}
	req.RequestID = fmt.Sprintf("%032x", 12)
	req.SourceSeq = int64(event.Seq)
	same, err := s.ChangeTask(ctx, "team", alice, req)
	if err != nil || same.ID != task.ID {
		t.Fatal("answer conversion duplicated task", same, err)
	}
	task, err = s.ChangeTask(ctx, "team", alice, complete)
	if err != nil || task.State != "completed" || task.CompletedAt == "" {
		t.Fatal(task, err)
	}
	follow := room.SubmitInput{TaskID: task.ID, ClientMessageID: bobID, Text: "再补充测试"}
	before, _ = s.LatestSeq(ctx, "team")
	if _, err = s.AcceptMessage(ctx, "team", alice, follow); err == nil {
		t.Fatal("submitted completed task")
	}
	after, _ = s.LatestSeq(ctx, "team")
	if before != after {
		t.Fatal("failed followup leaked a message")
	}
	reopen := room.TaskRequest{Action: "reopen", RequestID: fmt.Sprintf("%032x", 13), TaskID: task.ID, ExpectedRevision: task.Revision}
	task, err = s.ChangeTask(ctx, "team", bob, reopen)
	if err != nil || task.State != "review" {
		t.Fatal(task, err)
	}
	// Retrying an old completion receipt cannot close the reopened task again.
	task, err = s.ChangeTask(ctx, "team", alice, complete)
	if err != nil || task.State != "review" {
		t.Fatal(task, err)
	}
	accepted, err := s.AcceptMessage(ctx, "team", alice, follow)
	if err != nil {
		t.Fatal(err)
	}
	dup, err := s.AcceptMessage(ctx, "team", alice, follow)
	if err != nil || !dup.Duplicate {
		t.Fatal(dup, err)
	}
	changed := follow
	changed.TaskID = 0
	if _, err = s.AcceptMessage(ctx, "team", alice, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	req.SourceSeq = int64(accepted.Seq)
	req.RequestID = fmt.Sprintf("%032x", 14)
	same, err = s.ChangeTask(ctx, "team", alice, req)
	if err != nil || same.ID != task.ID {
		t.Fatal("followup created another task", same, err)
	}
	image, err := s.LoadRecoveryImage(ctx, "team")
	if err != nil || len(image.Queue) != 1 || image.Queue[0].Input.TaskID != task.ID {
		t.Fatal(image, err)
	}
	got, err = s.Tasks(ctx, "team", room.TaskRequest{Action: "get", TaskID: task.ID})
	if err != nil || len(got.Task.Runs) != 2 || got.Task.State != "queued" {
		t.Fatal(got, err)
	}
	if _, err = s.Tasks(ctx, "other-room", room.TaskRequest{Action: "get", TaskID: task.ID}); err == nil {
		t.Fatal("cross room read")
	}
}
func TestTaskPermissionsAndStaleCompletion(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedRoom(t, s)
	a, err := s.AppendNote(ctx, "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "待处理事项"})
	if err != nil {
		t.Fatal(err)
	}
	req := room.TaskRequest{Action: "convert", RequestID: fmt.Sprintf("%032x", 20), Title: "待处理", SourceSeq: int64(a.Seq)}
	for _, role := range []string{"visitor", "asker"} {
		if _, err = s.db.Exec(`INSERT INTO member_roles(room_id,uid,role)VALUES('team',1002,?) ON CONFLICT(room_id,uid)DO UPDATE SET role=excluded.role`, role); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ChangeTask(ctx, "team", bob, req); err == nil {
			t.Fatal("restricted conversion", role)
		}
	}
	task, err := s.ChangeTask(ctx, "team", alice, req)
	if err != nil {
		t.Fatal(err)
	}
	complete := room.TaskRequest{Action: "complete", RequestID: fmt.Sprintf("%032x", 21), TaskID: task.ID, ExpectedRevision: 0}
	if _, err = s.ChangeTask(ctx, "team", alice, complete); err == nil {
		t.Fatal("stale mutation succeeded")
	}
	complete.ExpectedRevision = task.Revision
	if _, err = s.ChangeTask(ctx, "team", bob, complete); err == nil {
		t.Fatal("asker completed task")
	}
	if _, err = s.AcceptMessage(ctx, "team", bob, room.SubmitInput{TaskID: task.ID, ClientMessageID: bobID, Text: "work"}); err == nil {
		t.Fatal("asker submitted task")
	}
	task, err = s.ChangeTask(ctx, "team", alice, complete)
	if err != nil || task.State != "completed" {
		t.Fatal(task, err)
	}
	got, err := s.Tasks(ctx, "team", room.TaskRequest{Action: "get", TaskID: task.ID})
	if err != nil || got.Task.SourceText != "待处理事项" {
		t.Fatal(got, err)
	}
}

func TestTaskRecoveryContinuationKeepsOriginalAndReplacement(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedRoom(t, s)
	beginAndReview(t, s, aliceID)
	var seq int64
	if err := s.db.QueryRow("SELECT accepted_seq FROM messages WHERE client_message_id=?", aliceID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	task, err := s.ChangeTask(ctx, "team", alice, room.TaskRequest{Action: "convert", RequestID: fmt.Sprintf("%032x", 30), SourceSeq: seq, Title: "恢复任务"})
	if err != nil || task.State != "needs_confirmation" {
		t.Fatal(task, err)
	}
	in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoveryContinue, ReplacementMessageID: bobID, Instruction: "检查实际状态后继续"}
	if _, err = s.ResolveReview(ctx, "team", bob, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Tasks(ctx, "team", room.TaskRequest{Action: "get", TaskID: task.ID})
	if err != nil || len(got.Task.Runs) != 2 || got.Task.State != "queued" {
		t.Fatal(got, err)
	}
	image, err := s.LoadRecoveryImage(ctx, "team")
	if err != nil || len(image.Queue) != 1 || image.Queue[0].Input.TaskID != task.ID {
		t.Fatal(image, err)
	}
	duplicate, err := s.ResolveReview(ctx, "team", bob, in)
	if err != nil || !duplicate.Duplicate {
		t.Fatal(duplicate, err)
	}
	got, err = s.Tasks(ctx, "team", room.TaskRequest{Action: "get", TaskID: task.ID})
	if err != nil || len(got.Task.Runs) != 2 {
		t.Fatal(got, err)
	}
}
func TestTasksSurviveDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/room.db"
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedRoom(t, s)
	a, err := s.AppendNote(ctx, "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "持久化任务"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.ChangeTask(ctx, "team", alice, room.TaskRequest{Action: "convert", RequestID: fmt.Sprintf("%032x", 40), SourceSeq: int64(a.Seq), Title: "检查"})
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.ChangeTask(ctx, "team", alice, room.TaskRequest{Action: "complete", RequestID: fmt.Sprintf("%032x", 41), TaskID: task.ID, ExpectedRevision: task.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Tasks(ctx, "team", room.TaskRequest{Action: "get", TaskID: task.ID})
	if err != nil || got.Task.State != "completed" || got.Task.CompletedAt != task.CompletedAt || len(got.Task.History) != 2 {
		t.Fatal(got, err)
	}
}
