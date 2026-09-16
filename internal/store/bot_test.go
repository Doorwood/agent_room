package store

import (
	"agent_romm/internal/room"
	"context"
	"strings"
	"testing"
)

func TestBotMappingRequiresActiveMembershipAndRole(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if err := s.InitAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BotMember(ctx, "team", alice.UID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BotMember(ctx, "team", bob.UID, false); err == nil {
		t.Fatal("historical member implicitly approved")
	}
	req, err := s.RequestJoin(ctx, "team", strings.Repeat("e", 64), "feishu-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	req, err = s.DecideJoinRole(ctx, "team", req.ID, "approve", "visitor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BotMember(ctx, "team", req.UID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BotMember(ctx, "team", req.UID, true); err == nil {
		t.Fatal("visitor can work")
	}
	if _, err = s.DecideJoinRole(ctx, "team", req.ID, "role", "roommate"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BotMember(ctx, "team", req.UID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideJoin(ctx, "team", req.ID, "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BotMember(ctx, "team", req.UID, false); err == nil {
		t.Fatal("revoked member can read")
	}
}
func TestBotHistoryIncludesPromptsAndOnlyFinalAnswers(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if _, err := s.AcceptMessage(ctx, "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "修复页面"}); err != nil {
		t.Fatal(err)
	}
	// Fixture inserts representative durable output; production never exposes
	// arbitrary tool output, commentary or private-turn tables through this query.
	for i, p := range []string{`{"payload":{"type":"agentMessage","phase":"final_answer","text":"页面已修复"}}`, `{"payload":{"type":"commandExecution","text":"secret tool output"}}`, `{"payload":{"type":"agentMessage","phase":"commentary","text":"private progress"}}`} {
		if _, err := s.db.Exec(`INSERT INTO room_events VALUES('team',?,0,'item/completed',?,'2026-09-10T00:00:00Z')`, 100+i, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.BotHistory(ctx, "team", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "修复页面") || !strings.Contains(got, "页面已修复") || strings.Contains(got, "secret") || strings.Contains(got, "private progress") {
		t.Fatal(got)
	}
	got, err = s.BotHistory(ctx, "team", "不存在")
	if err != nil || !strings.Contains(got, "没有匹配") {
		t.Fatal(got, err)
	}
}
