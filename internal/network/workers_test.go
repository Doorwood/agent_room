package network

import (
	"agent_romm/internal/workgroup"
	"context"
	"strings"
	"testing"
	"time"
)

func TestWorkerTransportAuthLeaseAndLargeResult(t *testing.T) {
	s, st, _ := testServer(t)
	b := workgroup.NewRemoteBroker()
	s.EnableWorkers(b)
	ctx := context.Background()
	ls := map[string]Launcher{}
	for i, role := range []string{"roommate", "visitor", "asker"} {
		l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: role, Token: strings.Repeat(string(rune('d'+i)), 64)}}
		c, r, e := l.dial(ctx)
		if e != nil {
			t.Fatal(e)
		}
		c.Close()
		if _, e = st.DecideJoinRole(ctx, s.room, r.RequestID, "approve", role); e != nil {
			t.Fatal(e)
		}
		ls[role] = l
	}
	req := workgroup.WorkerRequest{Action: "invite", ID: strings.Repeat("a", 32), Provider: "codex", AgentName: "代码评审", Mode: "review"}
	for _, role := range []string{"visitor", "asker"} {
		if _, e := ls[role].Worker(ctx, req); e == nil {
			t.Fatal("readonly member invited worker")
		}
	}
	invited, e := ls["roommate"].Worker(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := ls["roommate"].Worker(ctx, workgroup.WorkerRequest{Action: "health", ID: invited.ID, Healthy: true}); e != nil {
		t.Fatal(e)
	}
	if invited.Name != "roommate-代码评审" || b.Members()[0].Name != invited.Name {
		t.Fatal("owner name not bound", invited.Name)
	}
	done := make(chan error, 1)
	go func() {
		r, e := b.Run(ctx, b.Members()[0], workgroup.Assignment{HumanPrompt: "question", Prompt: "secret"})
		if e == nil && len(r.Text) != 20000 {
			t.Errorf("large result lost")
		}
		done <- e
	}()
	var poll workgroup.WorkerReply
	for i := 0; i < 50; i++ {
		poll, e = ls["roommate"].Worker(ctx, workgroup.WorkerRequest{Action: "poll", ID: invited.ID})
		if e != nil {
			t.Fatal(e)
		}
		if poll.JobID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if poll.Assignment == nil {
		t.Fatal("no job")
	}
	if strings.Contains(poll.Assignment.Prompt, "secret") {
		t.Fatal("capability leaked")
	}
	if _, e = ls["roommate"].Worker(ctx, workgroup.WorkerRequest{Action: "result", ID: invited.ID, JobID: poll.JobID, Text: strings.Repeat("x", 20000)}); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("receipt stalled")
	}
}
