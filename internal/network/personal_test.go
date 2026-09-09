package network

import (
	"agent_romm/internal/personal"
	"agent_romm/internal/resources"
	"agent_romm/internal/room"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestModelCreationRoutesToAuthenticatedSenderAndReturnsReceipt(t *testing.T) {
	s, st, dir := testServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	alice := resourceMember(t, s, "roommate", strings.Repeat("d", 64))
	bob := resourceMember(t, s, "roommate", strings.Repeat("e", 64))
	asker := resourceMember(t, s, "asker", strings.Repeat("f", 64))
	m, err := st.AuthenticateJoin(ctx, s.room, alice.Credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	id := room.ClientMessageID(strings.Repeat("a", 32))
	actor := room.Actor{UID: m.UID, Name: m.Name}
	if _, err := st.AcceptMessage(ctx, s.room, actor, room.SubmitInput{ClientMessageID: id, Text: "创建一个规划飞书文档"}); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginDispatch(ctx, s.room, id); err != nil {
		t.Fatal(err)
	}
	b := personal.New(func(ctx context.Context, uid room.UID, id room.ClientMessageID) error {
		return st.CheckPersonalSender(ctx, s.room, uid, id)
	})
	// Set initial mode before registering broker (live work prevents mode changes afterwards).
	if err := ResourceMode(ctx, dir, "personal", io.Discard); err != nil {
		t.Fatal(err)
	}
	s.EnablePersonal(b)
	cap, _ := b.Bind(id, actor)
	out := make(chan personal.Result, 1)
	go func() {
		body, _ := json.Marshal(map[string]string{"capability": cap, "title": "规划", "content": "仅给 Alice 的正文"})
		var response bytes.Buffer
		if e := PersonalCreate(ctx, dir, bytes.NewReader(body), &response); e != nil {
			t.Error(e)
			return
		}
		var result personal.Result
		if e := json.Unmarshal(response.Bytes(), &result); e != nil {
			t.Error(e)
			return
		}
		out <- result
	}()
	var p *personal.Request
	for p == nil && ctx.Err() == nil {
		reply, e := alice.Resources(ctx, ResourceRequest{Action: "personal-pending", ClientID: strings.Repeat("c", 32)}, nil)
		if e != nil {
			t.Fatal(e)
		}
		p = reply.Personal
		time.Sleep(time.Millisecond)
	}
	if p == nil {
		t.Fatal("sender received nothing")
	}
	if r, e := bob.Resources(ctx, ResourceRequest{Action: "personal-pending", ClientID: strings.Repeat("b", 32)}, nil); e != nil || r.Personal != nil {
		t.Fatal("Bob saw private content", r, e)
	}
	if _, e := asker.Resources(ctx, ResourceRequest{Action: "personal-pending", ClientID: strings.Repeat("f", 32)}, nil); e == nil {
		t.Fatal("asker allowed")
	}
	result := &personal.Result{ID: p.ID, Receipt: &resources.CreateReceipt{RequestID: p.ID, Title: p.Title, Account: resources.FeishuIdentity{Name: "Alice", OpenID: "ou_alice123456"}, State: "completed", URL: "https://example.feishu.cn/docx/Doc123456789"}}
	if _, e := bob.Resources(ctx, ResourceRequest{Action: "personal-result", ClientID: strings.Repeat("c", 32), PersonalResult: result}, nil); e == nil {
		t.Fatal("Bob replaced Alice result")
	}
	before, _ := st.LatestSeq(ctx, s.room)
	if _, e := alice.Resources(ctx, ResourceRequest{Action: "personal-result", ClientID: strings.Repeat("c", 32), PersonalResult: result}, nil); e != nil {
		t.Fatal(e)
	}
	select {
	case r := <-out:
		if r.Receipt.Account.OpenID != "ou_alice123456" {
			t.Fatal(r)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	after, _ := st.LatestSeq(ctx, s.room)
	if before != after {
		t.Fatal("private transport wrote shared history")
	}
	if err := ResourceMode(ctx, dir, "host", io.Discard); err == nil {
		t.Fatal("changed resource identity mid-turn")
	}
}
