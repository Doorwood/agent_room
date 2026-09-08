package daemon

import (
	"agent_romm/internal/identity"
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedPeer struct{ uid room.UID }

func (p fixedPeer) Resolve(net.Conn) (identity.Peer, error) { return identity.Peer{UID: p.uid}, nil }

type fixture struct {
	mu                   sync.Mutex
	reads, writes, calls int
	actor                room.Actor
	hub                  *Hub
	snapshot             func() room.Snapshot
	timeout              time.Duration
}

func (f *fixture) FindMember(_ context.Context, _ room.RoomID, uid room.UID) (room.Member, error) {
	if uid != 1002 {
		return room.Member{}, errors.New("unknown")
	}
	return room.Member{UID: uid, Name: "bob"}, nil
}
func (f *fixture) LatestSeq(context.Context, room.RoomID) (room.Seq, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return 0, nil
}
func (f *fixture) Events(context.Context, room.RoomID, room.Seq, room.Seq, int) ([]room.DurableEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return nil, nil
}
func (f *fixture) Connected(context.Context, room.ConnectionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	return nil
}
func (f *fixture) Ack(context.Context, room.ConnectionID, room.Seq) error           { return nil }
func (f *fixture) Disconnected(context.Context, room.ConnectionID, time.Time) error { return nil }
func (f *fixture) CloseStale(context.Context, room.RoomID, time.Time) error         { return nil }
func (f *fixture) Submit(_ context.Context, a room.Actor, _ room.SubmitInput) (room.Acceptance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.actor = a
	f.hub.PublishDurable(room.DurableEvent{Seq: 1, Kind: "message-accepted", Payload: []byte(`{}`)})
	return room.Acceptance{Seq: 1}, nil
}
func (f *fixture) Note(ctx context.Context, a room.Actor, i room.SubmitInput) (room.Acceptance, error) {
	return f.Submit(ctx, a, i)
}
func (f *fixture) Steer(context.Context, room.Actor, room.SteerInput) (room.Acceptance, error) {
	return room.Acceptance{}, nil
}
func (f *fixture) Cancel(context.Context, room.Actor, room.CancelInput) (room.Acceptance, error) {
	return room.Acceptance{}, nil
}
func (f *fixture) Resolve(context.Context, room.Actor, room.RecoverInput) error { return nil }
func (f *fixture) Snapshot(context.Context) (room.Snapshot, error) {
	if f.snapshot != nil {
		return f.snapshot(), nil
	}
	return room.Snapshot{}, nil
}
func startServer(t *testing.T, uid room.UID, f *fixture, configure ...func(*Dependencies)) (*Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ar-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	f.hub = NewHub(256, 16<<20)
	deps := Dependencies{Peers: fixedPeer{uid}, Members: f, Events: f, Connections: f, Coordinator: f, Hub: f.hub}
	for _, configure := range configure {
		configure(&deps)
	}
	timeout := f.timeout
	if timeout == 0 {
		timeout = time.Second
	}
	s, err := NewServer(Config{RoomID: "r", RoomName: "room", ProjectRoot: dir, ExecutionOwner: "owner", SocketPath: path, SocketGID: uint32(os.Getgid()), RequestTimeout: timeout, HandshakeTimeout: timeout, IdleTimeout: timeout}, deps)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- s.Serve(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		select {
		case err := <-result:
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("serve blocked")
		}
	})
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Lstat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no socket")
		}
		time.Sleep(time.Millisecond)
	}
	return s, path
}
func dial(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	return c
}

const reqID = "00000000000000000000000000000001"

func request(t *testing.T, c *net.UnixConn, method string, body any) {
	t.Helper()
	data, _ := json.Marshal(body)
	if err := protocol.NewWriter(c).Write(protocol.Envelope{Version: 1, Kind: protocol.KindRequest, ID: reqID, Method: method, Body: data}); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, c *net.UnixConn) protocol.Envelope {
	t.Helper()
	e, err := protocol.NewReader(c, protocol.MaxFrameBytes).Read()
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func hello(t *testing.T, c *net.UnixConn) {
	request(t, c, "hello", protocol.Hello{MinVersion: 1, MaxVersion: 1})
	if e := read(t, c); e.Method != "welcome" {
		t.Fatal(e)
	}
	if e := read(t, c); e.Method != "runtime-snapshot" {
		t.Fatal(e)
	}
}
func TestUnknownPeerGetsNoHistoryAndNoPersistence(t *testing.T) {
	f := &fixture{}
	_, path := startServer(t, 9999, f)
	c := dial(t, path)
	e := read(t, c)
	body, err := protocol.DecodeBody[WireError](e.Body)
	if err != nil || body.Code != "member-not-found" {
		t.Fatal(e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads != 0 || f.writes != 0 || f.calls != 0 {
		t.Fatal("unauthorized access")
	}
}
func TestPayloadCannotChangeBoundActor(t *testing.T) {
	f := &fixture{}
	_, path := startServer(t, 1002, f)
	c := dial(t, path)
	hello(t, c)
	request(t, c, "submit", map[string]any{"clientMessageId": reqID, "text": "hello", "uid": 1001, "author": "alice"})
	if e := read(t, c); e.Kind != protocol.KindError {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("spoof reached coordinator")
	}
	request(t, c, "submit", protocol.SubmitRequest{ClientMessageID: reqID, Text: "clean"})
	read(t, c)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 1 || f.actor.UID != 1002 || f.actor.Name != "bob" {
		t.Fatal(f.actor)
	}
}
func TestMalformedConnectionDoesNotAffectOtherSession(t *testing.T) {
	f := &fixture{}
	_, path := startServer(t, 1002, f)
	bad := dial(t, path)
	good := dial(t, path)
	hello(t, good)
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], protocol.MaxFrameBytes+1)
	bad.Write(prefix[:])
	if e := read(t, bad); e.Kind != protocol.KindError {
		t.Fatal(e)
	}
	request(t, good, "submit", protocol.SubmitRequest{ClientMessageID: reqID, Text: "clean"})
	a, b := read(t, good), read(t, good)
	if a.Kind != protocol.KindEvent && b.Kind != protocol.KindEvent {
		t.Fatal("missing event")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 1 || f.calls != 1 {
		t.Fatal(f.writes, f.calls)
	}
}
func TestShutdownReleasesHandshakePeer(t *testing.T) {
	f := &fixture{}
	s, path := startServer(t, 1002, f)
	_ = dial(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestReplaySnapshotRevisionFence(t *testing.T) {
	f := &fixture{}
	entered, release := make(chan struct{}), make(chan struct{})
	snapshotCalls := 0
	f.snapshot = func() room.Snapshot {
		snapshotCalls++
		if snapshotCalls == 1 {
			return room.Snapshot{}
		}
		close(entered)
		<-release
		return room.Snapshot{ProjectionRevision: 7, LiveItems: []room.LiveItemSnapshot{{ItemID: "item-1", Partial: "partial"}}}
	}
	_, path := startServer(t, 1002, f)
	c := dial(t, path)
	request(t, c, "hello", protocol.Hello{MinVersion: 1, MaxVersion: 1})
	read(t, c)
	<-entered
	f.hub.PublishTransient(room.TransientEvent{Revision: 7, Kind: "delta", Delta: "partial"})
	close(release)
	snapshot := read(t, c)
	if snapshot.Method != "runtime-snapshot" {
		t.Fatal(snapshot)
	}
	f.hub.PublishDurable(room.DurableEvent{Seq: 1, Kind: "item-completed", Payload: []byte(`{}`)})
	f.hub.PublishTransient(room.TransientEvent{Revision: 8, Kind: "delta", Delta: "new"})
	if got := read(t, c); got.Method != "item-completed" {
		t.Fatal("stale partial reappeared", got)
	}
	if got := read(t, c); got.Method != "delta" {
		t.Fatal(got)
	}
}
func TestSocketRefusesExistingPath(t *testing.T) {
	f := &fixture{}
	s, _ := startServer(t, 1002, f)
	cfg := s.cfg
	other, err := NewServer(cfg, s.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Serve(context.Background()); !errors.Is(err, ErrSocketPath) {
		t.Fatal(err)
	}
}

func TestShutdownReleasesBlockedWriter(t *testing.T) {
	f := &fixture{}
	s, path := startServer(t, 1002, f)
	c := dial(t, path)
	hello(t, c)
	f.hub.PublishTransient(room.TransientEvent{Revision: 1, Kind: "delta", Delta: strings.Repeat("x", 4<<20)})
	// Wait for encoding to finish and the large frame to reach the socket.
	// Leave its body unread so shutdown must release the blocked writer.
	if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var prefix [4]byte
	if _, err := io.ReadFull(c, prefix[:]); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(prefix[:]) < 4<<20 {
		t.Fatal("expected the large frame before testing writer shutdown")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidHandshakeDoesNotPersist(t *testing.T) {
	for _, body := range []any{map[string]any{"minVersion": 1, "maxVersion": 1, "uid": 1001}, protocol.Hello{MinVersion: 2, MaxVersion: 2}} {
		f := &fixture{}
		_, path := startServer(t, 1002, f)
		c := dial(t, path)
		request(t, c, "hello", body)
		if got := read(t, c); got.Kind != protocol.KindError {
			t.Fatal(got)
		}
		f.mu.Lock()
		if f.reads != 0 || f.writes != 0 || f.calls != 0 {
			t.Error("invalid handshake touched state")
		}
		f.mu.Unlock()
	}
}

func TestSocketRefusesFileAndSymlinkAndPreservesReplacement(t *testing.T) {
	f := &fixture{}
	s, path := startServer(t, 1002, f)
	// Moving the endpoint preserves the active listener while replacing its path.
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	other, _ := NewServer(s.cfg, s.deps)
	if err := other.Serve(context.Background()); !errors.Is(err, ErrSocketPath) {
		t.Fatal(err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatal("replacement removed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".moved", path); err != nil {
		t.Fatal(err)
	}
	other, _ = NewServer(s.cfg, s.deps)
	if err := other.Serve(context.Background()); !errors.Is(err, ErrSocketPath) {
		t.Fatal(err)
	}
}
