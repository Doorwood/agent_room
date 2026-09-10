package personal

import (
	"agent_romm/internal/resources"
	"agent_romm/internal/room"
	"context"
	"strings"
	"testing"
)

func TestAppendBoundTargetReceiptAndSender(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(nil)
	b.SetMode("personal")
	cap, _ := b.Bind(room.ClientMessageID(strings.Repeat("a", 32)), room.Actor{UID: 1001, Name: "alice"})
	target := "https://bytedance.sg.larkoffice.com/docx/BP2edVezXoSUNMx6KEjlXCUigHc"
	out := make(chan Result, 1)
	go func() {
		r, e := b.Append(ctx, cap, target, "补写", "完整正文")
		if e != nil {
			t.Error(e)
		}
		out <- r
	}()
	client := strings.Repeat("c", 32)
	p := pendingFor(t, b, 1001, client)
	if p.Target != target || p.Action != "feishu.append" {
		t.Fatal(p)
	}
	if other, _ := b.Next(ctx, 1002, strings.Repeat("d", 32)); other != nil {
		t.Fatal("cross sender")
	}
	receipt := &resources.CreateReceipt{RequestID: p.ID, Title: p.Title, Action: p.Action, Target: target, ContentSHA256: resources.ContentDigest(p.Content), Account: resources.FeishuIdentity{OpenID: "ou_alice123456"}, State: "completed", URL: target}
	receipt.Target = "https://example.feishu.cn/docx/AnotherDoc123"
	if b.Resolve(ctx, 1001, client, Result{ID: p.ID, Receipt: receipt}) == nil {
		t.Fatal("wrong target accepted")
	}
	receipt.Target = target
	receipt.ContentSHA256 = "wrong"
	if b.Resolve(ctx, 1001, client, Result{ID: p.ID, Receipt: receipt}) == nil {
		t.Fatal("wrong content accepted")
	}
	receipt.ContentSHA256 = resources.ContentDigest(p.Content)
	if b.Resolve(ctx, 1002, client, Result{ID: p.ID, Receipt: receipt}) == nil {
		t.Fatal("wrong sender accepted")
	}
	if err := b.Resolve(ctx, 1001, client, Result{ID: p.ID, Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	if (<-out).Receipt.URL != target {
		t.Fatal("missing URL")
	}
	if _, err := b.Append(ctx, "stale", target, "补写", "完整正文"); err == nil {
		t.Fatal("stale accepted")
	}
}
