package store

import (
	"agent_romm/internal/room"
	"context"
	"testing"
)

func TestResetMissingUnusedThreadKeepsNotesAndRoomIdentity(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if _, err := s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "empty-thread", CWD: "/srv/project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendNote(ctx, "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "keep this note"}); err != nil {
		t.Fatal(err)
	}
	active, err := s.HasAgentActivity(ctx, "team")
	if err != nil || active {
		t.Fatal(active, err)
	}
	reset, err := s.ResetUnusedThread(ctx, "team", "empty-thread")
	if err != nil || !reset {
		t.Fatal(reset, err)
	}
	image, err := s.LoadRecoveryImage(ctx, "team")
	if err != nil || image.ThreadID != "" || image.Status != room.RoomReady {
		t.Fatal(image, err)
	}
	events, err := s.Events(ctx, "team", 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "note/accepted" {
			found = true
		}
	}
	if !found {
		t.Fatal("note history lost")
	}
	if _, err = s.FindMember(ctx, "team", alice.UID); err != nil {
		t.Fatal("membership lost", err)
	}
}
func TestResetUnusedThreadRefusesAcceptedWorkAndChangedBinding(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if _, err := s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}); err != nil {
		t.Fatal(err)
	}
	if reset, err := s.ResetUnusedThread(ctx, "team", "wrong-thread"); err != nil || reset {
		t.Fatal(reset, err)
	}
	if _, err := s.AcceptMessage(ctx, "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "task"}); err != nil {
		t.Fatal(err)
	}
	active, err := s.HasAgentActivity(ctx, "team")
	if err != nil || !active {
		t.Fatal(active, err)
	}
	if reset, err := s.ResetUnusedThread(ctx, "team", "thread-1"); err != nil || reset {
		t.Fatal(reset, err)
	}
	image, err := s.LoadRecoveryImage(ctx, "team")
	if err != nil || image.ThreadID != "thread-1" {
		t.Fatal(image, err)
	}
}
