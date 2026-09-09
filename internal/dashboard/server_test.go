package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent_romm/internal/config"
	"agent_romm/internal/localprobe"
	"agent_romm/internal/network"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"net"
)

func catalogFixture(t *testing.T) Catalog {
	t.Helper()
	return Catalog{Home: t.TempDir(), Config: t.TempDir()}
}
func sessionFixture() string { return strings.Repeat("a", 32) + "." + strings.Repeat("b", 64) }
func request(t *testing.T, s *Server, route string, body any, origin string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", s.URL()+route, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.serve(w, r)
	return w
}
func await(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func TestDashboardLifecycleAndCredentialPersistence(t *testing.T) {
	catalog := catalogFixture(t)
	r, err := catalog.Add("127.0.0.1:7443", sessionFixture(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	original, _ := catalog.Credentials()
	var starts atomic.Int32
	connector := func(ctx context.Context, r Room, update Update) error {
		starts.Add(1)
		update("connected", "", "http://127.0.0.1:4567/view/")
		<-ctx.Done()
		return ctx.Err()
	}
	server, err := Start(context.Background(), catalog, connector)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	origin := "http://" + server.listener.Addr().String()
	for i := 0; i < 5; i++ {
		if w := request(t, server, "connect", map[string]string{"id": r.ID}, origin); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	await(t, func() bool { rows, _ := server.list(); return rows[0].Status == "connected" })
	if starts.Load() != 1 {
		t.Fatal("duplicate connection", starts.Load())
	}
	w := httptest.NewRecorder()
	server.serve(w, httptest.NewRequest("GET", server.URL()+"rooms", nil))
	if strings.Contains(w.Body.String(), original[0].Token) || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatal("credential leaked")
	}
	request(t, server, "disconnect", map[string]string{"id": r.ID}, origin)
	await(t, func() bool { rows, _ := server.list(); return rows[0].Status == "disconnected" && rows[0].URL == "" })
	if err := server.connect(r); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return starts.Load() == 2 })
	server.Close()
	saved, _ := catalog.Credentials()
	if saved[0] != original[0] {
		t.Fatal("credential changed")
	}
	fresh, err := Start(context.Background(), catalog, connector)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	rows, _ := fresh.list()
	if rows[0].Status != "disconnected" {
		t.Fatal("new dashboard must not auto-connect")
	}
}
func TestDashboardRejectsCrossOriginAndInvalidPaths(t *testing.T) {
	server, _ := Start(context.Background(), catalogFixture(t), func(context.Context, Room, Update) error { t.Fatal("must not connect"); return nil })
	defer server.Close()
	for _, origin := range []string{"", "https://evil.example"} {
		w := request(t, server, "add", map[string]string{}, origin)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	for _, path := range []string{"/", server.path + "../rooms"} {
		w := httptest.NewRecorder()
		server.serve(w, httptest.NewRequest("GET", "http://"+server.listener.Addr().String()+path, nil))
		if w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", server.URL()+"rooms", nil)
	r.Host = "evil.example"
	w := httptest.NewRecorder()
	server.serve(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = request(t, server, "add", map[string]string{"unexpected": "field"}, "http://"+server.listener.Addr().String())
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestOwnedAndJoinedDiscoveryAndCorruptCredential(t *testing.T) {
	catalog := catalogFixture(t)
	state := filepath.Join(catalog.Home, ".local/share/agent_room/projects/demo")
	private := filepath.Join(state, "private")
	os.MkdirAll(private, 0700)
	cfg := config.RuntimeConfig{RoomID: room.RoomID(strings.Repeat("c", 32)), ProjectRoot: "/projects/demo", RoomName: "Demo"}
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(private, "config.json"), b, 0600)
	os.WriteFile(filepath.Join(private, "network.json"), []byte(`{"address":"127.0.0.1:7443","listen":"0.0.0.0:7443"}`), 0600)
	_, pin, err := network.Certificate(private)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := catalog.List()
	if len(rows) != 1 || rows[0].Kind != "owned" {
		t.Fatal(rows)
	}
	r, err := catalog.Add("127.0.0.1:7443", string(cfg.RoomID)+"."+pin, "alice")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ = catalog.List()
	if len(rows) != 1 || rows[0].ID != r.ID || rows[0].Project != "/projects/demo" {
		t.Fatal(rows)
	}
	os.WriteFile(filepath.Join(catalog.credentialDir(), "broken.json"), []byte(`{`), 0600)
	rows, warnings := catalog.List()
	if len(rows) != 1 || len(warnings) != 1 {
		t.Fatal(rows, warnings)
	}
}

type discardMember struct{}

func (discardMember) ServeMember(ctx context.Context, c net.Conn, m room.Member) {
	defer c.Close()
	io.Copy(io.Discard, c)
}
func TestRealAdmissionDisconnectKeepsHostAndIdentity(t *testing.T) {
	catalog := catalogFixture(t)
	private := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(private, "room.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rid := room.RoomID(strings.Repeat("d", 32))
	err = st.InitializeRoom(ctx, store.RoomSeed{ID: rid, DisplayName: "test", HostID: "host", ProjectRoot: "/tmp", ExecutionOwnerUID: 1001, Members: []room.Member{{UID: 1001, Name: "owner"}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := network.Start(ctx, "127.0.0.1:0", private, rid, st, discardMember{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	r, err := catalog.Add(host.Address(), host.SessionID(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	dash, err := Start(ctx, catalog, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dash.Close()
	dash.connect(r)
	await(t, func() bool { rows, _ := dash.list(); return rows[0].Status == "pending" })
	dash.disconnect(r.ID)
	await(t, func() bool { rows, _ := dash.list(); return rows[0].Status == "disconnected" })
	// The host still accepts new admission connections; same credential is reused.
	dash.connect(r)
	await(t, func() bool { rows, _ := dash.list(); return rows[0].Status == "pending" })
}

func TestJoinedProjectPersistsAcrossDashboardRestartAndIdentities(t *testing.T) {
	c := catalogFixture(t)
	for _, name := range []string{"alice", "bob"} {
		if _, err := c.Add("127.0.0.1:7443", sessionFixture(), name); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := c.List()
	if rows[0].ProjectName != "" {
		t.Fatal("invented project name")
	}
	if err := c.RememberProject("127.0.0.1:7443", sessionFixture(), "/work/项目-room"); err != nil {
		t.Fatal(err)
	}
	restarted := Catalog{Home: c.Home, Config: c.Config}
	rows, _ = restarted.List()
	for _, r := range rows {
		if r.ProjectName != "项目-room" || r.Project != "/work/项目-room" {
			t.Fatalf("missing cached project: %+v", r)
		}
	}
	if _, err := c.Add("127.0.0.1:7444", sessionFixture(), "carol"); err != nil {
		t.Fatal(err)
	}
	rows, _ = c.List()
	for _, r := range rows {
		if r.Name == "carol" && r.Project != "" {
			t.Fatal("project leaked across hosts")
		}
	}
	if err := c.RememberProject("127.0.0.1:7443", sessionFixture(), "/work/renamed"); err != nil {
		t.Fatal(err)
	}
	rows, _ = restarted.List()
	for _, r := range rows {
		if r.Name != "carol" && r.ProjectName != "renamed" {
			t.Fatal("project did not refresh")
		}
	}
}

func TestRemoveDisconnectsAndStaysRemovedUntilExplicitAdd(t *testing.T) {
	c := catalogFixture(t)
	r, err := c.Add("127.0.0.1:7443", sessionFixture(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	original, _ := c.Credentials()
	stopped := make(chan struct{})
	s, err := Start(context.Background(), c, func(ctx context.Context, r Room, update Update) error { <-ctx.Done(); close(stopped); return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.connect(r); err != nil {
		t.Fatal(err)
	}
	out := request(t, s, "remove", map[string]string{"id": r.ID}, "http://"+s.listener.Addr().String())
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("connection not canceled")
	}
	rows, _ := s.list()
	if len(rows) != 0 {
		t.Fatal("deleted active room reappeared")
	}
	restart := Catalog{Home: c.Home, Config: c.Config}
	rows, _ = restart.List()
	if len(rows) != 0 {
		t.Fatal("deleted room reappeared after restart")
	}
	if err = s.connect(r); err == nil {
		t.Fatal("stale connection resurrected room")
	}
	if _, err = c.Add(r.Address, r.Session, r.Name); err != nil {
		t.Fatal(err)
	}
	rows, _ = restart.List()
	if len(rows) != 1 {
		t.Fatal("explicit add did not restore room")
	}
	credentials, _ := c.Credentials()
	if credentials[0].Token != original[0].Token {
		t.Fatal("lost approved identity")
	}
}

func TestBrowserJoinReusesIdentityAndConnection(t *testing.T) {
	catalog := catalogFixture(t)
	var starts atomic.Int32
	server, err := Start(context.Background(), catalog, func(ctx context.Context, r Room, update Update) error {
		starts.Add(1)
		update("pending", "private detail", "http://127.0.0.1/private/")
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	req := localprobe.JoinRequest{Address: "127.0.0.1:7443", Session: sessionFixture(), Name: "web-user"}
	if _, err := server.joinFromBrowser(req, false); err != nil {
		t.Fatal(err)
	}
	creds, _ := catalog.Credentials()
	if len(creds) != 0 {
		t.Fatal("status created identity")
	}
	for i := 0; i < 2; i++ {
		if _, err := server.joinFromBrowser(req, true); err != nil {
			t.Fatal(err)
		}
	}
	await(t, func() bool { return starts.Load() == 1 })
	creds, _ = catalog.Credentials()
	if len(creds) != 1 {
		t.Fatal("duplicate identity")
	}
	await(t, func() bool { reply, _ := server.joinFromBrowser(req, false); return reply.State == "pending" })
	reply, _ := server.joinFromBrowser(req, false)
	b, _ := json.Marshal(reply)
	if string(b) != `{"state":"pending"}` {
		t.Fatal("leaked private state", string(b))
	}
}
