package workgroup

import (
	"agent_romm/internal/room"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRemoteWorkerOwnershipHandoffAndSecretBoundary(t *testing.T) {
	b := NewRemoteBroker()
	inv, e := b.Handle(7, WorkerRequest{Action: "invite", ID: strings.Repeat("a", 32), Provider: "cursor", Mode: "review"})
	if _, err := b.Handle(7, WorkerRequest{Action: "health", ID: inv.ID, Healthy: true}); err != nil {
		t.Fatal(err)
	}
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Handle(8, WorkerRequest{Action: "poll", ID: inv.ID}); e == nil {
		t.Fatal("foreign member claimed worker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		r, e := b.Run(ctx, b.Members()[0], Assignment{Prompt: "HOST_SECRET_CAPABILITY", HumanPrompt: "read test", ProjectRoot: "/host/private", HumanUID: 9, StepID: "review", StepPrompt: "review code", DependsOn: []string{"build"}, Prior: []Result{{StepID: "build", AgentID: "codex.b", Text: "built"}}})
		if e != nil {
			return
		}
		done <- r
	}()
	var poll WorkerReply
	for i := 0; i < 100; i++ {
		poll, _ = b.Handle(7, WorkerRequest{Action: "poll", ID: inv.ID})
		if poll.Assignment != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if poll.Assignment == nil || strings.Contains(poll.Assignment.Prompt, "HOST_SECRET") || poll.Assignment.ProjectRoot != "" || poll.Assignment.HumanUID != 9 {
		t.Fatal(poll)
	}
	if poll.Assignment.StepID != "review" || poll.Assignment.StepPrompt != "review code" || len(poll.Assignment.Prior) != 1 || poll.Assignment.Prior[0].StepID != "build" {
		t.Fatal("dependency handoff lost", poll.Assignment)
	}
	again, _ := b.Handle(7, WorkerRequest{Action: "poll", ID: inv.ID})
	if again.Assignment != nil {
		t.Fatal("claimed work was redelivered")
	}
	if _, e = b.Handle(8, WorkerRequest{Action: "result", ID: inv.ID, JobID: poll.JobID, Text: "forged"}); e == nil {
		t.Fatal("foreign result accepted")
	}
	if _, e = b.Handle(7, WorkerRequest{Action: "result", ID: inv.ID, JobID: "wrong", Text: "forged"}); e == nil {
		t.Fatal("wrong job accepted")
	}
	if _, e = b.Handle(7, WorkerRequest{Action: "result", ID: inv.ID, JobID: poll.JobID, Text: "proof"}); e != nil {
		t.Fatal(e)
	}
	select {
	case result := <-done:
		if result.Text != "proof" {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal("result lost")
	}
}
func TestLeaseLossAfterClaimIsUnknown(t *testing.T) {
	b := NewRemoteBroker()
	inv, _ := b.Handle(7, WorkerRequest{Action: "invite", ID: strings.Repeat("b", 32), Provider: "claude-code", Mode: "work"})
	if _, err := b.Handle(7, WorkerRequest{Action: "health", ID: inv.ID, Healthy: true}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := b.Run(context.Background(), b.Members()[0], Assignment{}); done <- e }()
	for i := 0; i < 100; i++ {
		p, _ := b.Handle(7, WorkerRequest{Action: "poll", ID: inv.ID})
		if p.JobID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	b.Handle(7, WorkerRequest{Action: "leave", ID: inv.ID})
	select {
	case e := <-done:
		if e != room.ErrDeliveryUnknown {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lease not enforced")
	}
}

func TestNamedCodexInvitationBindsOwnerAndKeepsStableID(t *testing.T) {
	b := NewRemoteBroker()
	owner := room.Member{UID: 42, Name: "lumos"}
	req := WorkerRequest{Action: "invite", ID: strings.Repeat("b", 32), Provider: "codex", Mode: "review", AgentName: "  代码评审  "}
	reply, err := b.HandleMember(owner, req)
	if _, e := b.HandleMember(owner, WorkerRequest{Action: "health", ID: reply.ID, Healthy: true}); e != nil {
		t.Fatal(e)
	}
	if err != nil || reply.Name != "lumos-代码评审" || b.Members()[0].Name != reply.Name {
		t.Fatal(reply, err)
	}
	again, err := b.HandleMember(owner, req)
	if err != nil || again.ID != reply.ID {
		t.Fatal(again, err)
	}
	req.AgentName = "另一名称"
	if _, err = b.HandleMember(owner, req); err == nil {
		t.Fatal("renamed duplicate invitation accepted")
	}
	if _, err = b.HandleMember(room.Member{UID: 43, Name: "lumos"}, WorkerRequest{Action: "poll", ID: reply.ID}); err == nil {
		t.Fatal("name spoof gained ownership")
	}
	for _, name := range []string{"bad\nname", "bad\u202ename", strings.Repeat("字", 41)} {
		req.ID = strings.Repeat("c", 32)
		req.AgentName = name
		if _, err = b.HandleMember(owner, req); err == nil {
			t.Fatal("invalid name accepted")
		}
	}
	req.AgentName = ""
	req.ID = strings.Repeat("d", 32)
	reply, err = b.HandleMember(owner, req)
	if err != nil || reply.Name != "lumos-Codex" {
		t.Fatal(reply, err)
	}
}
