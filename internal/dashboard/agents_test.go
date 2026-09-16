package dashboard

import (
	"agent_romm/internal/network"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"agent_romm/internal/workgroup"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDashboardInvitesLocalAgentAndReturnsResultOverTLS(t *testing.T) {
	for _, scope := range []string{"local", "host"} {
		t.Run(scope, func(t *testing.T) { testAgentScope(t, scope) })
	}
}
func testAgentScope(t *testing.T, scope string) {
	hostRoot := t.TempDir()
	if e := exec.Command("git", "init", hostRoot).Run(); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(hostRoot, "host-only.txt"), []byte("HOST_PROJECT_CONTENT"), 0600)
	if scope == "host" {
		os.WriteFile(filepath.Join(hostRoot, "large.txt"), []byte(strings.Repeat("x", 3<<20)), 0600)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "cursor-agent"), []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then printf "fixture 1.0";exit 0;fi
if [ -f challenge.txt ]; then
 if [ ! -f "$(dirname "$0")/health-enabled" ]; then
  cat >/dev/null
  printf '%s' '{"type":"result","result":"FAIL"}'
  exit 0
 fi
 input=$(cat)
 case "$input" in *result.txt*) cp challenge.txt result.txt;; esac
 printf '{"type":"result","result":"%s"}' "$(cat challenge.txt)"
else
 cat > received-prompt.txt
 printf '%s' '{"type":"result","subtype":"success","result":"LOCAL_CURSOR_PROOF","is_error":false}'
fi
`), 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx := context.Background()
	private := t.TempDir()
	st, e := store.Open(ctx, filepath.Join(private, "room.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	rid := room.RoomID(strings.Repeat("d", 32))
	e = st.InitializeRoom(ctx, store.RoomSeed{ID: rid, DisplayName: "test", HostID: "host", ProjectRoot: "/host/project", ExecutionOwnerUID: 1001, Members: []room.Member{{UID: 1001, Name: "owner"}}})
	if e != nil {
		t.Fatal(e)
	}
	host, e := network.Start(ctx, "127.0.0.1:0", private, rid, st, discardMember{})
	if e != nil {
		t.Fatal(e)
	}
	defer host.Close()
	broker := workgroup.NewProjectRemoteBroker(hostRoot, t.TempDir())
	host.EnableWorkers(broker)
	catalog := catalogFixture(t)
	r, e := catalog.Add(host.Address(), host.SessionID(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	cred, e := network.CredentialFor(catalog.credentialDir(), r.Address, r.Session, r.Name)
	if e != nil {
		t.Fatal(e)
	}
	join, e := st.RequestJoin(ctx, rid, cred.Token, "alice", "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DecideJoinRole(ctx, rid, join.ID, "approve", "roommate"); e != nil {
		t.Fatal(e)
	}
	dash, e := Start(ctx, catalog, func(ctx context.Context, r Room, update Update) error {
		update("connected", "", "")
		<-ctx.Done()
		return ctx.Err()
	})
	if e != nil {
		t.Fatal(e)
	}
	defer dash.Close()
	dash.connect(r)
	await(t, func() bool { rows, _ := dash.list(); return rows[0].Status == "connected" })
	localRoot := t.TempDir()
	project, mode := localRoot, "review"
	if scope == "host" {
		project = ""
		mode = "work"
	}
	agent, e := dash.inviteAgent(ctx, inviteBody{WorkspaceScope: scope, ID: r.ID, Invitation: strings.Repeat("a", 32), Provider: "cursor", AgentName: "代码助手", Project: project, Mode: mode})
	if e != nil {
		t.Fatal(e)
	}
	await(t, func() bool {
		dash.mu.Lock()
		defer dash.mu.Unlock()
		return dash.localAgents[agent.ID].State == "error"
	})
	if len(broker.Members()) != 0 {
		t.Fatal("failed health check advertised worker")
	}
	os.WriteFile(filepath.Join(bin, "health-enabled"), []byte("ready"), 0600)
	dash.mu.Lock()
	dash.localAgents[agent.ID].recheck <- struct{}{}
	dash.mu.Unlock()
	await(t, func() bool { return len(broker.Members()) == 1 })
	if agent.WorkerID == "" || agent.Name != "alice-代码助手" || broker.Members()[0].Name != agent.Name {
		t.Fatal("no worker identity")
	}
	run, stop := context.WithTimeout(ctx, 8*time.Second)
	defer stop()
	result, e := broker.Run(run, broker.Members()[0], workgroup.Assignment{HumanUID: 1001, Prompt: "SECRET_HOST_CAPABILITY", HumanPrompt: "read a local file", ProjectRoot: "/host/project"})
	if e != nil || !strings.Contains(result.Text, "LOCAL_CURSOR_PROOF") {
		t.Fatal(result, e)
	}
	resultRoot := localRoot
	if scope == "host" {
		resultRoot = result.WorkspacePath
		if resultRoot == "" || resultRoot == hostRoot || resultRoot == localRoot {
			t.Fatal("missing isolated Host result")
		}
		if _, e := os.Stat(filepath.Join(hostRoot, "received-prompt.txt")); !os.IsNotExist(e) {
			t.Fatal("Host source modified")
		}
		large, err := os.Stat(filepath.Join(resultRoot, "large.txt"))
		if err != nil || large.Size() != 3<<20 {
			t.Fatal("large snapshot transfer failed", err)
		}
		original, e := os.ReadFile(filepath.Join(resultRoot, "host-only.txt"))
		if e != nil || string(original) != "HOST_PROJECT_CONTENT" {
			t.Fatal("Host source missing from result")
		}
	}
	raw, e := os.ReadFile(filepath.Join(resultRoot, "received-prompt.txt"))
	if e != nil || strings.Contains(string(raw), "SECRET_HOST_CAPABILITY") || !strings.Contains(string(raw), "read a local file") {
		t.Fatal("local boundary failed", e)
	}
	dash.disconnect(r.ID)
	await(t, func() bool {
		dash.mu.Lock()
		defer dash.mu.Unlock()
		return dash.localAgents[agent.ID].State == "offline"
	})
	if len(broker.Members()) != 0 {
		t.Fatal("disconnected agent still available")
	}
}
