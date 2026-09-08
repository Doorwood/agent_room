package network

import (
	"agent_romm/internal/questions"
	"agent_romm/internal/room"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRolesGateUploadsAndIsolatedQuestionVisibility(t *testing.T) {
	s, st, dir := testServer(t)
	s.questions.Close()
	var err error
	s.questions, err = questions.Open(dir, func(ctx context.Context, actor room.Actor, id room.ClientMessageID, text string) (room.QuestionAnswer, error) {
		return room.QuestionAnswer{Session: "host-thread", Text: "answer"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	launchers := map[string]Launcher{}
	for i, role := range []string{"roommate", "visitor", "asker"} {
		l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: role, Token: strings.Repeat(string(rune('a'+i)), 64)}}
		c, reply, err := l.dial(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if _, err = st.DecideJoinRole(ctx, s.room, reply.RequestID, "approve", role); err != nil {
			t.Fatal(err)
		}
		launchers[role] = l
	}
	before, err := st.LatestSeq(ctx, s.room)
	if err != nil {
		t.Fatal(err)
	}
	for role, l := range launchers {
		info, err := l.Query(ctx, QueryRequest{Action: "info"})
		if err != nil || info.Role != role {
			t.Fatal(info, err)
		}
		_, err = l.Upload(ctx, "x.txt", 1, bytes.NewReader([]byte("x")))
		if (role == "roommate") != (err == nil) {
			t.Fatalf("%s upload: %v", role, err)
		}
	}
	asker := launchers["asker"]
	reply, err := asker.Query(ctx, QueryRequest{Action: "ask", ID: strings.Repeat("1", 32), Text: "Explain this concept"})
	if err != nil || len(reply.Entries) != 1 {
		t.Fatal(reply, err)
	}
	if _, err = launchers["visitor"].Query(ctx, QueryRequest{Action: "list"}); err == nil {
		t.Fatal("visitor saw isolated questions")
	}
	all, err := launchers["roommate"].Query(ctx, QueryRequest{Action: "list"})
	if err != nil || len(all.Entries) != 1 {
		t.Fatal(all, err)
	}
	if _, err = launchers["roommate"].Query(ctx, QueryRequest{Action: "ask", ID: strings.Repeat("2", 32), Text: "work"}); err == nil {
		t.Fatal("roommate entered asker session")
	}
	filtered, err := launchers["roommate"].Query(ctx, QueryRequest{Action: "list", UID: 999})
	if err != nil || len(filtered.Entries) != 0 {
		t.Fatal("member filter", filtered, err)
	}
	own, err := asker.Query(ctx, QueryRequest{Action: "list", UID: 999})
	if err != nil || len(own.Entries) != 1 {
		t.Fatal("asker forged filter", own, err)
	}
	after, _ := st.LatestSeq(ctx, s.room)
	if before != after {
		t.Fatal("question changed main transcript")
	}
}
