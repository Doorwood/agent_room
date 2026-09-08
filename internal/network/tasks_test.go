package network

import (
	"agent_romm/internal/room"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestTasksTransportAuthorizationAndPersistence(t *testing.T) {
	s, st, _ := testServer(t)
	ctx := context.Background()
	members := map[string]Launcher{}
	for i, role := range []string{"roommate", "visitor", "asker"} {
		l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: role, Token: strings.Repeat(string(rune('d'+i)), 64)}}
		c, r, err := l.dial(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if _, err = st.DecideJoinRole(ctx, s.room, r.RequestID, "approve", role); err != nil {
			t.Fatal(err)
		}
		members[role] = l
	}
	member, err := st.AuthenticateJoin(ctx, s.room, members["roommate"].Credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	source, err := st.AppendNote(ctx, s.room, room.Actor{UID: member.UID, Name: member.Name}, room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("8", 32)), Text: "review this"})
	if err != nil {
		t.Fatal(err)
	}
	req := room.TaskRequest{Action: "convert", RequestID: fmt.Sprintf("%032x", 1), SourceSeq: int64(source.Seq), Title: "Review"}
	for _, role := range []string{"visitor", "asker"} {
		if _, err = members[role].Tasks(ctx, req); err == nil {
			t.Fatal(role, "wrote task")
		}
	}
	reply, err := members["roommate"].Tasks(ctx, req)
	if err != nil || reply.Task == nil {
		t.Fatal(reply, err)
	}
	for _, role := range []string{"visitor", "asker", "roommate"} {
		got, err := members[role].Tasks(ctx, room.TaskRequest{Action: "get", TaskID: reply.Task.ID})
		if err != nil || got.Task.SourceText != "review this" {
			t.Fatal(got, err)
		}
		got, err = members[role].Tasks(ctx, room.TaskRequest{Action: "list"})
		if err != nil || len(got.Tasks) != 1 {
			t.Fatal(got, err)
		}
	}
}
