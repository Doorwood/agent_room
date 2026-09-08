package store

import (
	"agent_romm/internal/room"
	"context"
	"strings"
	"testing"
)

func TestRepairThreadAtomicAuditPreservesWork(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if _, e := s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "old", CWD: "/srv/project"}); e != nil {
		t.Fatal(e)
	}
	beginAndReview(t, s, aliceID)
	acceptedPrompt(t, s, bobID)
	reason := room.RepairReason{Code: "owner-use", DetailDigest: digest}
	candidate := room.ThreadSnapshot{ID: "new", CWD: "/srv/project"}
	if _, e := s.db.Exec(`CREATE TRIGGER reject_repair BEFORE INSERT ON room_events WHEN NEW.kind = 'thread/repaired' BEGIN SELECT RAISE(ABORT, 'fault'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RepairThread(ctx, "team", candidate, reason); e == nil {
		t.Fatal("expected event fault")
	}
	image, e := s.LoadRecoveryImage(ctx, "team")
	if e != nil || image.ThreadID != "old" {
		t.Fatalf("binding not atomic: %+v %v", image, e)
	}
	s.db.Exec(`DROP TRIGGER reject_repair`)
	event, e := s.RepairThread(ctx, "team", candidate, reason)
	if e != nil {
		t.Fatal(e)
	}
	for _, value := range []string{"old", "new", "owner-use", digest} {
		if !strings.Contains(string(event.Payload), value) {
			t.Fatalf("audit missing %s", value)
		}
	}
	image, e = s.LoadRecoveryImage(ctx, "team")
	if e != nil || image.ThreadID != "new" || image.Status != room.RoomRecovering || len(image.Queue) != 1 {
		t.Fatalf("%+v %v", image, e)
	}
	state, _ := messageState(t, s, aliceID)
	if state != room.RequestNeedsReview {
		t.Fatal("lost uncertainty")
	}
}
