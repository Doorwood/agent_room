package personal

import (
	"agent_romm/internal/gitpush"
	"agent_romm/internal/room"
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestPushBindsSenderClientChunkAndReceipt(t *testing.T) {
	root := gitFixture(t)
	head := testGit(t, root, "rev-parse", "HEAD")
	b := New(nil)
	b.SetMode("personal")
	cap, err := b.Bind("message-push", room.Actor{UID: 42, Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	in := gitpush.Input{Repository: "alice/project", Branch: "main", Commit: head}
	result := make(chan Result, 1)
	go func() {
		r, e := b.Push(ctx, cap, root, in)
		if e != nil {
			t.Error(e)
		}
		result <- r
	}()
	client := strings.Repeat("a", 32)
	var p *Request
	for p == nil && ctx.Err() == nil {
		p, err = b.Next(ctx, 42, client)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if p == nil || p.Push == nil {
		t.Fatal("no push request")
	}
	if other, e := b.Next(ctx, 43, strings.Repeat("b", 32)); e != nil || other != nil {
		t.Fatal("other sender saw push")
	}
	for _, who := range []struct {
		uid    room.UID
		client string
	}{{43, client}, {42, strings.Repeat("b", 32)}} {
		if _, e := b.PushChunk(ctx, who.uid, who.client, p.ID, 0); e == nil {
			t.Fatal("foreign client got pack")
		}
	}
	if _, e := b.PushChunk(ctx, 42, client, p.ID, 1); e == nil {
		t.Fatal("unaligned chunk allowed")
	}
	var data bytes.Buffer
	for data.Len() < p.Push.PackSize {
		chunk, e := b.PushChunk(ctx, 42, client, p.ID, data.Len())
		if e != nil {
			t.Fatal(e)
		}
		data.Write(chunk)
	}
	if e := p.Push.CheckPack(data.Bytes()); e != nil {
		t.Fatal(e)
	}
	receipt := gitpush.Receipt{ID: p.ID, Input: in, Account: gitpush.Account{ID: 42, Login: "alice"}, State: "completed", URL: "https://github.com/" + in.Repository + "/commit/" + in.Commit}
	wrong := receipt
	wrong.Input.Commit = strings.Repeat("b", 40)
	if b.Resolve(ctx, 42, client, Result{ID: p.ID, PushReceipt: &wrong}) == nil {
		t.Fatal("wrong commit accepted")
	}
	if b.Resolve(ctx, 42, client, Result{ID: p.ID, PushReceipt: &receipt, GitIdentity: &GitIdentity{Name: "Alice", Email: "a@example.test"}}) == nil {
		t.Fatal("mixed receipt accepted")
	}
	if err = b.Resolve(ctx, 42, client, Result{ID: p.ID, PushReceipt: &receipt}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-result:
		if r.PushReceipt == nil || r.PushReceipt.Input != in {
			t.Fatal(r)
		}
	case <-ctx.Done():
		t.Fatal("no receipt")
	}
	if _, err = b.PushChunk(ctx, 42, client, p.ID, 0); err == nil {
		t.Fatal("pack available after completion")
	}
	r, err := b.Push(ctx, cap, root, in)
	if err != nil || r.PushReceipt == nil {
		t.Fatal("retry lost receipt", err)
	}
	b.Invalidate()
	if _, err = b.Push(ctx, cap, root, in); err == nil {
		t.Fatal("stale cap accepted")
	}
}
