package store

import (
	"agent_romm/internal/room"
	"context"
	"path/filepath"
	"testing"
)

func TestPrivateFrontierHidesLostStartReplyAcrossRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "room.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	seedRoom(t, s)
	before, _ := s.LatestSeq(ctx, "team")
	if err := s.BeginPrivateTurn(ctx, "team", "thread", []room.TurnID{"main"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := s.PrivateTurns(ctx, "team", "thread", []room.TurnSnapshot{{ID: "main", State: room.RequestCompleted}, {ID: "lost-private", State: room.RequestRunning}})
	if err == nil {
		t.Fatalf("running unknown turn admitted: %v", hidden)
	}
	turns := []room.TurnSnapshot{{ID: "main", State: room.RequestCompleted}, {ID: "lost-private", State: room.RequestCompleted}}
	hidden, err = s.PrivateTurns(ctx, "team", "thread", turns)
	if err != nil || !hidden["lost-private"] || hidden["main"] {
		t.Fatal(hidden, err)
	}
	turns = append(turns, room.TurnSnapshot{ID: "later-main", State: room.RequestCompleted})
	hidden, err = s.PrivateTurns(ctx, "team", "thread", turns)
	if err != nil || !hidden["lost-private"] || hidden["later-main"] {
		t.Fatal(hidden, err)
	}
	after, _ := s.LatestSeq(ctx, "team")
	if before != after {
		t.Fatal("private journal entered shared event stream")
	}
}
