package feishubot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	work             int
	reads            int
	revoked          bool
	writeDenied      bool
	lastID, lastText string
}

func (f *fakeBackend) Authorize(_ context.Context, _ uint32, work bool) error {
	if f.revoked || (work && f.writeDenied) {
		return errors.New("denied")
	}
	return nil
}
func (f *fakeBackend) Status(context.Context) (string, error) { f.reads++; return "idle", nil }
func (f *fakeBackend) History(context.Context, string) (string, error) {
	f.reads++
	return "private room history", nil
}
func (f *fakeBackend) Work(_ context.Context, _ uint32, id, text string) (string, error) {
	f.work++
	f.lastID = id
	f.lastText = text
	return "queued", nil
}

type fakeTransport struct {
	recipients, texts []string
	chats             int
	fail              bool
}

func (f *fakeTransport) Send(_ context.Context, u, s, id string) error {
	f.recipients = append(f.recipients, u)
	f.texts = append(f.texts, s)
	if f.fail {
		return errors.New("send unknown")
	}
	return nil
}
func (f *fakeTransport) ChatHistory(context.Context, string) (string, error) {
	f.chats++
	return "external group data", nil
}
func testConfig() Config {
	return Config{RoomID: strings.Repeat("a", 32), Profile: "room-bot", AppID: "cli_1234567890", Name: "Codex Agent", Bindings: []Binding{{OpenID: "ou_alice123456", UID: 123, AllowWork: true, OriginChats: []string{"oc_group123456"}, ReadableChats: []string{"oc_readable123"}}}}
}
func event(text string) Event {
	return Event{Type: "im.message.receive_v1", EventID: "event123", MessageID: "om_message123456", SenderID: "ou_alice123456", ChatID: "oc_private12345", ChatType: "p2p", MessageType: "text", Content: text, CreateTime: fmt.Sprint(time.Now().UnixMilli())}
}
func TestBotAuthorizationAndCommands(t *testing.T) {
	for _, scenario := range []string{"work", "visitor", "disabled", "unknown-user", "wrong-group", "group", "read-allowed", "read-denied", "ordinary-chat", "stale", "revoked", "media"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			config := testConfig()
			backend := &fakeBackend{}
			tr := &fakeTransport{}
			e := event("/room 工作 优化列表页")
			switch scenario {
			case "visitor":
				backend.writeDenied = true
			case "disabled":
				config.Bindings[0].AllowWork = false
			case "unknown-user":
				e.SenderID = "ou_intruder1234"
			case "wrong-group":
				e.ChatType = "group"
			case "group":
				e.ChatType = "group"
				e.ChatID = "oc_group123456"
			case "read-allowed":
				e.Content = "/room 群消息 oc_readable123"
			case "read-denied":
				e.Content = "/room 群消息 oc_othergroup123"
			case "ordinary-chat":
				e.Content = "帮我修改代码"
			case "stale":
				e.CreateTime = "1"
			case "revoked":
				backend.revoked = true
			case "media":
				e.MessageType = "post"
			}
			b, err := New(config, filepath.Join(t.TempDir(), "state"), backend, tr)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if err = b.Handle(ctx, e); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if scenario == "work" || scenario == "group" {
				expected = 1
			}
			if backend.work != expected {
				t.Fatal("incorrect work", backend.work)
			}
			if scenario == "read-allowed" && tr.chats != 1 {
				t.Fatal("missing chat history")
			}
			if scenario != "read-allowed" && tr.chats != 0 {
				t.Fatal("read unapproved chat")
			}
			for _, u := range tr.recipients {
				if u != config.Bindings[0].OpenID {
					t.Fatal("public/cross user reply", u)
				}
			}
			if expected == 1 && (backend.lastText != "优化列表页" || len(backend.lastID) != 32) {
				t.Fatal(backend)
			}
		})
	}
}
func TestBotReplayAndUnknownNeverRepeatWork(t *testing.T) {
	ctx := context.Background()
	backend := &fakeBackend{}
	tr := &fakeTransport{fail: true}
	dir := filepath.Join(t.TempDir(), "state")
	b, err := New(testConfig(), dir, backend, tr)
	if err != nil {
		t.Fatal(err)
	}
	e := event("/room 工作 fix")
	if b.Handle(ctx, e) == nil {
		t.Fatal("lost delivery not surfaced")
	}
	b.Close()
	b, err = New(testConfig(), dir, backend, tr)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	e.EventID = "redelivery"
	e.Content = "/room 工作 changed"
	if err = b.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	if backend.work != 1 || len(tr.texts) != 1 {
		t.Fatal("replayed unknown event")
	}
}
func TestBotConfigRejectsAmbiguousIdentity(t *testing.T) {
	c := testConfig()
	c.Bindings = append(c.Bindings, c.Bindings[0])
	if c.Validate() == nil {
		t.Fatal("duplicate mapping")
	}
	c = testConfig()
	c.Profile = ""
	if c.Validate() == nil {
		t.Fatal("ambient profile accepted")
	}
	c = testConfig()
	c.Bindings[0].ReadableChats = []string{"https://evil.test"}
	if c.Validate() == nil {
		t.Fatal("untyped target")
	}
	c = testConfig()
	c.Name = strings.Repeat("x", 81)
	if c.Validate() == nil {
		t.Fatal("unbounded name")
	}
}

func TestBotRateLimitAndRevocationBeforeDisclosure(t *testing.T) {
	ctx := context.Background()
	backend := &fakeBackend{}
	tr := &fakeTransport{}
	b, err := New(testConfig(), filepath.Join(t.TempDir(), "state"), backend, tr)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 0; i < 11; i++ {
		e := event("/room 状态")
		e.MessageID = fmt.Sprintf("om_message%08d", i)
		if err = b.Handle(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if len(tr.texts) != 10 || backend.reads != 10 {
		t.Fatal("rate limit not applied", len(tr.texts), backend.reads)
	}
	backend.revoked = true
	e := event("/room 历史")
	e.MessageID = "om_revoked123456"
	if err = b.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	if backend.reads != 10 {
		t.Fatal("revoked read")
	}
}
