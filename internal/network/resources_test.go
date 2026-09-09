package network

import (
	"agent_romm/internal/resources"
	"context"
	"crypto/tls"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func resourceMember(t *testing.T, s *Server, role, token string) Launcher {
	t.Helper()
	l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: role + token[:1], Token: token}}
	c, r, err := l.dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err = s.store.DecideJoinRole(context.Background(), s.room, r.RequestID, "approve", role); err != nil {
		t.Fatal(err)
	}
	return l
}
func TestResourceModesIdentityIsolationAndNoSharedHistory(t *testing.T) {
	s, st, dir := testServer(t)
	ctx := context.Background()
	alice := resourceMember(t, s, "roommate", strings.Repeat("d", 64))
	bob := resourceMember(t, s, "roommate", strings.Repeat("e", 64))
	visitor := resourceMember(t, s, "visitor", strings.Repeat("f", 64))
	asker := resourceMember(t, s, "asker", strings.Repeat("a", 64))
	var hostCalls atomic.Int32
	s.EnableResources(func(context.Context, resources.Request) (string, error) {
		hostCalls.Add(1)
		return "host evidence", nil
	}, func(ctx context.Context, q, text string) (string, error) { return "answer: " + text, nil })
	req := ResourceRequest{Action: "query", Mode: "host", Resource: resources.Request{Action: "github.repo", Target: "a/b"}, Question: "explain"}
	info, err := alice.Resources(ctx, ResourceRequest{Action: "info"}, nil)
	if err != nil || info.Mode != "host" {
		t.Fatal(info, err)
	}
	before, _ := st.LatestSeq(ctx, s.room)
	got, err := alice.Resources(ctx, req, nil)
	if err != nil || got.Text != "host evidence" {
		t.Fatal(got, err)
	}
	if _, err = asker.Resources(ctx, req, nil); err == nil {
		t.Fatal("asker used host resources")
	}
	if err = ResourceMode(ctx, dir, "personal", io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err = alice.Resources(ctx, req, nil); err == nil {
		t.Fatal("stale host mode used")
	}
	req.Mode = "personal"
	for _, l := range []Launcher{alice, bob, asker} {
		text := "private " + l.Credential.Name
		got, err = l.Resources(ctx, req, func(context.Context, resources.Request) (string, error) { return text, nil })
		if err != nil || got.Answer != "answer: "+text {
			t.Fatal(got, err)
		}
	}
	if _, err = visitor.Resources(ctx, req, func(context.Context, resources.Request) (string, error) {
		t.Error("visitor invoked tool")
		return "", nil
	}); err == nil {
		t.Fatal("visitor read resources")
	}
	if hostCalls.Load() != 1 {
		t.Fatal("personal mode fell back to host")
	}
	after, _ := st.LatestSeq(ctx, s.room)
	if before != after {
		t.Fatal("private resources entered shared history")
	}
	if _, err = alice.Resources(ctx, req, nil); err == nil {
		t.Fatal("missing client allowed")
	}
}
func TestModeSwitchCancelsClientExecution(t *testing.T) {
	s, _, dir := testServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l := resourceMember(t, s, "roommate", strings.Repeat("b", 64))
	if err := ResourceMode(ctx, dir, "personal", io.Discard); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := l.Resources(ctx, ResourceRequest{Action: "query", Mode: "personal", Resource: resources.Request{Action: "github.repo", Target: "a/b"}}, func(ctx context.Context, r resources.Request) (string, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return "", ctx.Err()
		})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("did not start")
	}
	if err := ResourceMode(ctx, dir, "host", io.Discard); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("client did not stop")
	}
	if err := <-done; err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestResourceModePersistsAndInvalidModeFailsClosed(t *testing.T) {
	s, st, dir := testServer(t)
	if err := ResourceMode(context.Background(), dir, "personal", io.Discard); err != nil {
		t.Fatal(err)
	}
	s.Close()
	next, err := Start(context.Background(), "127.0.0.1:0", dir, s.room, st, echoSession{})
	if err != nil {
		t.Fatal(err)
	}
	if next.resourceMode != "personal" {
		t.Fatal("mode did not persist")
	}
	next.Close()
	if err = os.WriteFile(filepath.Join(dir, "resource-mode"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if next, err = Start(context.Background(), "127.0.0.1:0", dir, s.room, st, echoSession{}); err == nil {
		next.Close()
		t.Fatal("invalid mode fell back")
	}
}
func TestResourceClientRejectsBroadenedAndRepeatedRequests(t *testing.T) {
	for _, variant := range []string{"target", "mode", "repeat", "title", "content", "account", "forged-done"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			cert, pin, err := Certificate(dir)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := listener.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(2 * time.Second))
				var hello Hello
				if readJSON(c, &hello) != nil {
					return
				}
				writeJSON(c, Reply{State: "approved"})
				var req ResourceRequest
				if readResource(c, &req) != nil {
					return
				}
				response := ResourceReply{Type: "execute", Mode: "personal", Resource: req.Resource}
				switch variant {
				case "title":
					response.Resource.Title = "changed"
				case "content":
					response.Resource.Content = "changed"
				case "account":
					response.Resource.AccountID = "ou_other123456"
				case "forged-done":
					response.Type = "done"
					response.Text = `{"state":"completed"}`
				}
				if variant == "target" {
					response.Resource.Target = "other/private"
				}
				if variant == "mode" {
					response.Mode = "host"
				}
				writeQuery(c, response)
				if variant == "repeat" {
					var result ResourceReply
					if readResource(c, &result) != nil {
						return
					}
					writeQuery(c, response)
				}
			}()
			l := Launcher{Credential{Address: listener.Addr().String(), Session: strings.Repeat("a", 32) + "." + pin, Name: "alice", Token: strings.Repeat("b", 64)}}
			calls := 0
			_, err = l.Resources(ctx, ResourceRequest{Action: "query", Mode: "personal", Resource: resources.Request{Action: "feishu.create", Title: "title", Content: "body", AccountID: "ou_alice123456", RequestID: strings.Repeat("c", 32)}}, func(context.Context, resources.Request) (string, error) { calls++; return "evidence", nil })
			if err == nil {
				t.Fatal("unexpected host request accepted")
			}
			want := 0
			if variant == "repeat" {
				want = 1
			}
			if calls != want {
				t.Fatal("unauthorized execution", calls)
			}
			<-done
		})
	}
}

func TestPersonalCreationRequiresRoommateAndOriginClient(t *testing.T) {
	s, st, dir := testServer(t)
	ctx := context.Background()
	alice := resourceMember(t, s, "roommate", strings.Repeat("d", 64))
	bob := resourceMember(t, s, "roommate", strings.Repeat("e", 64))
	asker := resourceMember(t, s, "asker", strings.Repeat("f", 64))
	visitor := resourceMember(t, s, "visitor", strings.Repeat("a", 64))
	s.EnableResources(func(context.Context, resources.Request) (string, error) {
		t.Error("Host executed personal write")
		return "host", nil
	}, nil)
	req := ResourceRequest{Action: "query", Mode: "host", Resource: resources.Request{Action: "feishu.create", Title: "Title", Content: "Body", RequestID: strings.Repeat("c", 32), AccountID: "ou_alice123456"}}
	if _, err := alice.Resources(ctx, req, nil); err == nil {
		t.Fatal("host write allowed")
	}
	if err := ResourceMode(ctx, dir, "personal", io.Discard); err != nil {
		t.Fatal(err)
	}
	req.Mode = "personal"
	before, _ := st.LatestSeq(ctx, s.room)
	for _, l := range []Launcher{alice, bob, asker, visitor} {
		calls := 0
		got, err := l.Resources(ctx, req, func(context.Context, resources.Request) (string, error) {
			calls++
			return "receipt for " + l.Credential.Name, nil
		})
		allowed := l.Credential.Name == alice.Credential.Name || l.Credential.Name == bob.Credential.Name
		if allowed {
			if err != nil || calls != 1 || got.Text != "receipt for "+l.Credential.Name {
				t.Fatal(got, err, calls)
			}
		} else if err == nil || calls != 0 {
			t.Fatal("read-only role created", err, calls)
		}
	}
	after, _ := st.LatestSeq(ctx, s.room)
	if before != after {
		t.Fatal("creation leaked into shared history")
	}
	req.Question = "do more work"
	if _, err := alice.Resources(ctx, req, func(context.Context, resources.Request) (string, error) {
		t.Error("write with model instruction executed")
		return "", nil
	}); err == nil {
		t.Fatal("write included model instruction")
	}
}
