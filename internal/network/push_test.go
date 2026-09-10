package network

import (
	"agent_romm/internal/gitpush"
	"agent_romm/internal/personal"
	"agent_romm/internal/room"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersonalPushChunkTransferChecksTLSMemberAndRole(t *testing.T) {
	s, st, dir := testServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	alice := resourceMember(t, s, "roommate", strings.Repeat("a", 64))
	bob := resourceMember(t, s, "roommate", strings.Repeat("b", 64))
	asker := resourceMember(t, s, "asker", strings.Repeat("c", 64))
	m, err := st.AuthenticateJoin(ctx, s.room, alice.Credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = ResourceMode(ctx, dir, "personal", io.Discard); err != nil {
		t.Fatal(err)
	}
	b := personal.New(nil)
	s.EnablePersonal(b)
	cap, _ := b.Bind("push-message", room.Actor{UID: m.UID, Name: m.Name})
	root := t.TempDir()
	command := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal(string(out), e)
		}
		return strings.TrimSpace(string(out))
	}
	command("init", "--template=")
	data := make([]byte, 260000)
	rand.Read(data)
	os.WriteFile(filepath.Join(root, "blob.bin"), data, 0600)
	command("add", "blob.bin")
	command("commit", "-m", "fixture")
	in := gitpush.Input{Repository: "alice/project", Branch: "main", Commit: command("rev-parse", "HEAD")}
	done := make(chan personal.Result, 1)
	go func() {
		r, e := b.Push(ctx, cap, root, in)
		if e != nil {
			t.Error(e)
		}
		done <- r
	}()
	client := strings.Repeat("e", 32)
	var p *personal.Request
	for p == nil && ctx.Err() == nil {
		r, e := alice.Resources(ctx, ResourceRequest{Action: "personal-pending", ClientID: client}, nil)
		if e != nil {
			t.Fatal(e)
		}
		p = r.Personal
		if p == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if p == nil || p.Push == nil {
		t.Fatal("no pending push")
	}
	for _, peer := range []Launcher{bob, asker} {
		if _, e := peer.Resources(ctx, ResourceRequest{Action: "personal-push-chunk", ClientID: client, PushID: p.ID}, nil); e == nil {
			t.Fatal("foreign member read commit pack")
		}
	}
	var pack []byte
	for len(pack) < p.Push.PackSize {
		r, e := alice.Resources(ctx, ResourceRequest{Action: "personal-push-chunk", ClientID: client, PushID: p.ID, PushOffset: len(pack)}, nil)
		if e != nil {
			t.Fatal(e)
		}
		pack = append(pack, r.PushData...)
	}
	if len(pack) <= gitpush.ChunkSize || p.Push.CheckPack(pack) != nil {
		t.Fatal("large pack transport failed")
	}
	receipt := gitpush.Receipt{ID: p.ID, Input: in, Account: gitpush.Account{ID: 42, Login: "alice"}, State: "completed", URL: "https://github.com/" + in.Repository + "/commit/" + in.Commit}
	_, err = alice.Resources(ctx, ResourceRequest{Action: "personal-result", ClientID: client, PersonalResult: &personal.Result{ID: p.ID, PushReceipt: &receipt}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		raw, _ := json.Marshal(r)
		if r.PushReceipt == nil || !strings.Contains(string(raw), in.Commit) {
			t.Fatal(r)
		}
	case <-ctx.Done():
		t.Fatal("no result")
	}
}
