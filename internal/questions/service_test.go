package questions

import (
	"agent_romm/internal/room"
	"context"
	"strings"
	"testing"
)

func TestSharedRunnerReusesHostSessionAndDoesNotResendHistory(t *testing.T) {
	t.Setenv("AGENT_ROOM_ASK_API_KEY", "")
	t.Setenv("AGENT_ROOM_ASK_MODEL", "")
	calls := 0
	s, err := Open(t.TempDir(), func(ctx context.Context, actor room.Actor, id room.ClientMessageID, text string) (room.QuestionAnswer, error) {
		calls++
		if text != "current question" {
			t.Error("reinjected history", text)
		}
		return room.QuestionAnswer{Session: "host-thread", Text: "answer"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Ready() {
		t.Fatal("requires API key")
	}
	for i, uid := range []room.UID{1, 2} {
		id := strings.Repeat(string(rune('a'+i)), 32)
		a, err := s.Ask(context.Background(), room.Member{UID: uid, Name: "asker"}, id, "current question")
		if err != nil || a.Session != "host-thread" {
			t.Fatal(a, err)
		}
		if _, err = s.Ask(context.Background(), room.Member{UID: uid, Name: "asker"}, id, "current question"); err != nil {
			t.Fatal(err)
		}
	}
	own, _ := s.List(context.Background(), 1, false, 0)
	all, _ := s.List(context.Background(), 1, true, 0)
	if len(own) != 1 || len(all) != 2 {
		t.Fatal("history visibility")
	}
	if calls != 2 {
		t.Fatal("retried generation", calls)
	}
}

func TestQuestionTimestampsAndLegacyMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir, func(context.Context, room.Actor, room.ClientMessageID, string) (room.QuestionAnswer, error) {
		return room.QuestionAnswer{Session: "thread", Text: "answer"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Ask(ctx, room.Member{UID: 1, Name: "asker"}, strings.Repeat("a", 32), "question")
	if err != nil || a.CreatedAt == "" || a.CompletedAt == "" {
		t.Fatal(a, err)
	}
	b, err := s.Ask(ctx, room.Member{UID: 1, Name: "asker"}, strings.Repeat("a", 32), "question")
	if err != nil || b.CreatedAt != a.CreatedAt || b.CompletedAt != a.CompletedAt {
		t.Fatal(b, err)
	}
	// Emulate the pre-timestamp schema with existing completed data.
	if _, err = s.db.Exec("ALTER TABLE questions DROP COLUMN created_at; ALTER TABLE questions DROP COLUMN completed_at;"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entries, err := s.List(ctx, 1, false, 0)
	if err != nil || len(entries) != 1 || entries[0].CreatedAt != "" || entries[0].Answer != "answer" {
		t.Fatal(entries, err)
	}
}
