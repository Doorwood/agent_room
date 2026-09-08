package network

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent_romm/internal/room"
	"agent_romm/internal/store"
)

type echoSession struct{}

func (echoSession) ServeMember(ctx context.Context, c net.Conn, m room.Member) {
	defer c.Close()
	_ = json.NewEncoder(c).Encode(m)
	_, _ = io.Copy(io.Discard, c)
}
func testServer(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ar-net-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dir, "room.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rid := room.RoomID(strings.Repeat("a", 32))
	if err = st.InitializeRoom(ctx, store.RoomSeed{ID: rid, DisplayName: "test", HostID: "host", ProjectRoot: "/tmp", ExecutionOwnerUID: 1001, Members: []room.Member{{UID: 1001, Name: "owner"}}}); err != nil {
		t.Fatal(err)
	}
	s, err := Start(ctx, "127.0.0.1:0", dir, rid, st, echoSession{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, st, dir
}
func TestApprovalGatesStreamAndRevokeDisconnects(t *testing.T) {
	s, _, dir := testServer(t)
	ctx := context.Background()
	l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: "visitor", Token: strings.Repeat("b", 64)}}
	c, r, err := l.dial(ctx)
	if err != nil || r.State != "pending" {
		t.Fatal(r, err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if b, e := io.ReadAll(c); e != nil || len(b) != 0 {
		t.Fatalf("pending read data %q %v", b, e)
	}
	c.Close()
	var out bytes.Buffer
	if err = Manage(ctx, dir, "approve", r.RequestID, &out); err != nil {
		t.Fatal(err)
	}
	c, r, err = l.dial(ctx)
	if err != nil || r.State != "approved" {
		t.Fatal(r, err)
	}
	defer c.Close()
	var member room.Member
	if err = json.NewDecoder(c).Decode(&member); err != nil || member.Name != "visitor" || member.UID < 1000000000 {
		t.Fatal(member, err)
	}
	if err = Manage(ctx, dir, "revoke", r.RequestID, &out); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if b, e := io.ReadAll(c); e != nil || len(b) != 0 {
		t.Fatalf("revoked stream %q %v", b, e)
	}
	c, r, err = l.dial(ctx)
	if err != nil || r.State != "revoked" {
		t.Fatal(r, err)
	}
	c.Close()
}
func TestPinSessionAndTokenAreBound(t *testing.T) {
	s, st, dir := testServer(t)
	ctx := context.Background()
	l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: "member", Token: strings.Repeat("c", 64)}}
	c, r, err := l.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err = st.DecideJoin(ctx, s.room, r.RequestID, "approve"); err != nil {
		t.Fatal(err)
	}
	wrong := l
	wrong.Credential.Session = strings.Repeat("d", 32) + "." + strings.Split(s.SessionID(), ".")[1]
	c, r, err = wrong.dial(ctx)
	if err != nil || r.State != "invalid-session" {
		t.Fatal(r, err)
	}
	c.Close()
	wrong = l
	wrong.Credential.Session = strings.Split(s.SessionID(), ".")[0] + "." + strings.Repeat("f", 64)
	if c, _, err = wrong.dial(ctx); err == nil {
		c.Close()
		t.Fatal("wrong certificate accepted")
	}
	wrong = l
	wrong.Credential.Token = strings.Repeat("e", 64)
	c, r, err = wrong.dial(ctx)
	if err != nil || r.State != "pending" {
		t.Fatal(r, err)
	}
	c.Close()
	before := s.SessionID()
	s.Close()
	s2, err := Start(ctx, "127.0.0.1:0", dir, s.room, st, echoSession{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.SessionID() != before {
		t.Fatal("session changed after restart")
	}
	l.Credential.Address = s2.Address()
	c, r, err = l.dial(ctx)
	if err != nil || r.State != "approved" {
		t.Fatal(r, err)
	}
	c.Close()
}
func TestOversizedHandshakeAndCredentialPersistence(t *testing.T) {
	s, _, _ := testServer(t)
	_, pin, _ := ParseSession(s.SessionID())
	c, err := tls.Dial("tcp", s.Address(), PinnedTLS(pin))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], maxHello+1)
	if _, err = c.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if b, e := io.ReadAll(c); e != nil || len(b) != 0 {
		t.Fatalf("oversize read %q %v", b, e)
	}
	dir := filepath.Join(t.TempDir(), "credentials")
	a, err := CredentialFor(dir, s.Address(), s.SessionID(), "my-name")
	if err != nil {
		t.Fatal(err)
	}
	b, err := CredentialFor(dir, s.Address(), s.SessionID(), "my-name")
	if err != nil || a != b {
		t.Fatal(b, err)
	}
}
