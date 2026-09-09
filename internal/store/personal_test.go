package store

import (
	"agent_romm/internal/room"
	"context"
	"testing"
)

func TestPersonalSenderMustOwnActiveRequest(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	acceptedPrompt(t, s, aliceID)
	if s.CheckPersonalSender(ctx, "team", alice.UID, aliceID) == nil {
		t.Fatal("queued request allowed")
	}
	if err := s.BeginDispatch(ctx, "team", aliceID); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPersonalSender(ctx, "team", alice.UID, aliceID); err != nil {
		t.Fatal(err)
	}
	if s.CheckPersonalSender(ctx, "team", alice.UID+1, aliceID) == nil {
		t.Fatal("wrong sender allowed")
	}
	if _, err := s.FailDispatch(ctx, "team", aliceID, room.FailureOutcome{State: room.RequestFailed, ErrorCode: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if s.CheckPersonalSender(ctx, "team", alice.UID, aliceID) == nil {
		t.Fatal("finished request allowed")
	}
}
