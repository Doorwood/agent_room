package personal

import (
	"agent_romm/internal/resources"
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func pendingFor(t *testing.T, b *Broker, uid room.UID, client string) *Request {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p, e := b.Next(context.Background(), uid, client)
		if e != nil {
			t.Fatal(e)
		}
		if p != nil {
			return p
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no pending request")
	return nil
}
func TestSenderBoundCreationPermissionAndIdempotency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(nil)
	b.SetMode("personal")
	cap, _ := b.Bind(room.ClientMessageID(strings.Repeat("a", 32)), room.Actor{UID: 1001, Name: "alice"})
	out := make(chan Result, 2)
	go func() {
		r, e := b.Call(ctx, cap, "Plan", "private body")
		if e != nil {
			t.Error(e)
		}
		out <- r
	}()
	p := pendingFor(t, b, 1001, strings.Repeat("c", 32))
	if other, _ := b.Next(ctx, 1002, strings.Repeat("d", 32)); other != nil {
		t.Fatal("Bob saw Alice content")
	}
	if other, _ := b.Next(ctx, 1001, strings.Repeat("e", 32)); other != nil {
		t.Fatal("second device claimed same write")
	}
	receipt := &resources.CreateReceipt{RequestID: p.ID, Title: p.Title, Account: resources.FeishuIdentity{Name: "Alice", OpenID: "ou_alice123456"}, State: "completed", URL: "https://example.feishu.cn/docx/Doc123456789"}
	result := Result{ID: p.ID, Receipt: receipt}
	if b.Resolve(ctx, 1002, strings.Repeat("c", 32), result) == nil {
		t.Fatal("Bob resolved Alice write")
	}
	if b.Resolve(ctx, 1001, strings.Repeat("e", 32), result) == nil {
		t.Fatal("nonclaimant resolved write")
	}
	if err := b.Resolve(ctx, 1001, strings.Repeat("c", 32), result); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-out:
		if r.Receipt.Account.OpenID != receipt.Account.OpenID {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("model did not receive result")
	}
	replay, err := b.Call(ctx, cap, "Plan", "private body")
	if err != nil || replay.Receipt.URL != receipt.URL {
		t.Fatal(replay, err)
	}
	if p, _ := b.Next(ctx, 1001, strings.Repeat("c", 32)); p != nil {
		t.Fatal("completed operation dispatched again")
	}
	_, _ = b.Bind(room.ClientMessageID(strings.Repeat("b", 32)), room.Actor{UID: 1002, Name: "bob"})
	if _, err := b.Call(ctx, cap, "Plan", "body"); err == nil {
		t.Fatal("old sender capability accepted")
	}
}
func TestCancellationAndModeChangeStopPendingWrites(t *testing.T) {
	for _, change := range []string{"cancel", "mode", "next-turn"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := New(nil)
			b.SetMode("personal")
			cap, _ := b.Bind(room.ClientMessageID(strings.Repeat("a", 32)), room.Actor{UID: 1, Name: "alice"})
			done := make(chan struct{})
			go func() { defer close(done); b.Call(ctx, cap, "Title", "body") }()
			p := pendingFor(t, b, 1, strings.Repeat("c", 32))
			switch change {
			case "cancel":
				cancel()
			case "mode":
				b.SetMode("host")
			case "next-turn":
				b.Bind(room.ClientMessageID(strings.Repeat("b", 32)), room.Actor{UID: 2, Name: "bob"})
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("wait not cancelled")
			}
			if next, _ := b.Next(context.Background(), 1, strings.Repeat("c", 32)); next != nil {
				t.Fatal("cancelled request still executable", p.ID)
			}
		})
	}
}
func TestReceiptRejectsMismatchedPayloadAndDenialDoesNotLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(nil)
	b.SetMode("personal")
	cap, _ := b.Bind(room.ClientMessageID(strings.Repeat("a", 32)), room.Actor{UID: 1, Name: "alice"})
	out := make(chan Result, 1)
	go func() { r, _ := b.Call(ctx, cap, "Title", "body"); out <- r }()
	p := pendingFor(t, b, 1, strings.Repeat("c", 32))
	if b.Resolve(ctx, 1, strings.Repeat("c", 32), Result{ID: p.ID, Receipt: &resources.CreateReceipt{RequestID: p.ID, Title: "changed", State: "completed"}}) == nil {
		t.Fatal("mismatch accepted")
	}
	if err := b.Resolve(ctx, 1, strings.Repeat("c", 32), Result{ID: p.ID, Error: "secret CLI token"}); err != nil {
		t.Fatal(err)
	}
	result := <-out
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "secret") || result.Receipt != nil {
		t.Fatal(string(raw))
	}
}

type recordingAgent struct {
	room.Agent
	text string
}

func (a *recordingAgent) StartTurn(_ context.Context, _ room.ThreadID, _ room.ClientMessageID, text string) (room.TurnID, error) {
	a.text = text
	return "turn-1", nil
}
func TestNaturalLanguageTurnGetsBoundPersonalCommand(t *testing.T) {
	inner := &recordingAgent{}
	b := New(nil)
	b.SetMode("personal")
	a := &Agent{Agent: inner, Broker: b, Executable: "/tmp/agent room", State: "/tmp/state"}
	_, err := a.StartTurnFor(context.Background(), "thread", room.ClientMessageID(strings.Repeat("a", 32)), room.Actor{UID: 42, Name: "alice"}, "[participant: bob]\n创建一个规划飞书文档")
	if err != nil || !strings.Contains(inner.text, "personal-create --state") || !strings.Contains(inner.text, "禁止调用 Host") || !strings.Contains(inner.text, "创建一个规划飞书文档") {
		t.Fatal(inner.text, err)
	}
	if !strings.Contains(inner.text, "personal-commit --state") || !strings.Contains(inner.text, "仅要求优化、修复或修改代码不代表允许 commit") || !strings.Contains(inner.text, "绝不能改用 Host 凭据") || !strings.Contains(inner.text, "personal-push --state") {
		t.Fatal("Git routing policy missing")
	}
	if b.actor.UID != 42 || b.actor.Name != "alice" {
		t.Fatal("text spoofed sender")
	}
	if err := a.SteerTurn(context.Background(), "thread", "turn", "Bob asks for a doc"); err == nil {
		t.Fatal("steer bypassed sender binding")
	}
	b.SetMode("host")
	a.StartTurnFor(context.Background(), "thread", room.ClientMessageID(strings.Repeat("b", 32)), room.Actor{UID: 43, Name: "bob"}, "normal work")
	if inner.text != "normal work" {
		t.Fatal("changed host behavior")
	}
}
