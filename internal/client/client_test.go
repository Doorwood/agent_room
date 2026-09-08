package client

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type launchFunc func(context.Context, string) (*Connection, error)

func (f launchFunc) Start(ctx context.Context, s string) (*Connection, error) { return f(ctx, s) }

type memoryCursors struct{ cursor Cursor }

func (m *memoryCursors) Load(s string) (Cursor, error) {
	if m.cursor.Target == "" {
		m.cursor.Target = s
	}
	return m.cursor, nil
}
func (m *memoryCursors) Save(c Cursor) error { m.cursor = c; return nil }

type brokenRandom struct{}

func (brokenRandom) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
func TestIDsAndCommandEnvelopes(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id, e := NewID(rand.Reader)
		if e != nil || !protocol.ValidMessageID(id) || seen[id] {
			t.Fatal(id, e)
		}
		seen[id] = true
	}
	if _, e := NewID(brokenRandom{}); e == nil {
		t.Fatal("entropy failure hidden")
	}
	for _, s := range []string{"/steer now", "/cancel"} {
		cmd, _ := ParseCommand(s)
		if _, _, e := cmd.Envelope(rand.Reader, ""); e == nil {
			t.Fatal("missing active accepted")
		}
	}
	cmd, _ := ParseCommand("/note human only")
	e, m, err := cmd.Envelope(rand.Reader, "")
	if err != nil || !m || e.Method != "note" {
		t.Fatal(e, m, err)
	}
	body, _ := protocol.DecodeBody[protocol.SubmitRequest](e.Body)
	if body.ClientMessageID == e.ID {
		t.Fatal("IDs reused")
	}
	cmd.Text = strings.Repeat("\x00", 20000)
	if _, _, err = cmd.Envelope(rand.Reader, ""); err == nil {
		t.Fatal("encoded limit not applied")
	}
}
func TestSafeTextAndProjectionReplacement(t *testing.T) {
	if got := SafeText("\x1b[2J\r\u202e\u200bhi\n\t"); strings.ContainsAny(got, "\x1b\r\u202e\u200b") || !strings.HasSuffix(got, "hi\n\t") {
		t.Fatal(got)
	}
	p := projection{}
	p.snapshot(protocol.RuntimeSnapshot{ProjectionRevision: 5, ActiveTurnID: "new", LiveItems: []protocol.LiveItem{{ThreadID: "t", TurnID: "r", ItemID: "i", Partial: "old"}}})
	var out bytes.Buffer
	_ = p.transient(room.TransientEvent{Revision: 4, Kind: "item-delta", Delta: "ignore"}, &out)
	if out.Len() != 0 {
		t.Fatal("stale delta")
	}
	_ = p.durable(room.DurableEvent{Seq: 1, Kind: "item/completed", Payload: json.RawMessage(`{"thread_id":"t","turn_id":"r","item_id":"i","payload":{"text":"done"}}`)}, &out)
	if len(p.partial) != 0 {
		t.Fatal("partial retained")
	}
	p.snapshot(protocol.RuntimeSnapshot{})
	if p.active != "" {
		t.Fatal("active not cleared")
	}
}
func TestCursorAtomicAndIdentity(t *testing.T) {
	s := FileCursors{Directory: t.TempDir()}
	c := Cursor{Target: "host", RoomID: "room", LastAppliedSeq: 12}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	got, e := s.Load("host")
	if e != nil || got != c {
		t.Fatal(got, e)
	}
	path, _ := s.path("host")
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	other, e := s.Load("other")
	if e != nil || other.LastAppliedSeq != 0 {
		t.Fatal(other, e)
	}
}
func sendFrame(c net.Conn, kind protocol.Kind, id, method string, body any, seq *uint64) error {
	b, _ := json.Marshal(body)
	return protocol.NewWriter(c).Write(protocol.Envelope{Version: 1, Kind: kind, ID: id, Method: method, Body: b, Seq: seq})
}
func welcome(c net.Conn, r *protocol.Reader, roomID string, high uint64) (protocol.Hello, error) {
	e, err := r.Read()
	if err != nil {
		return protocol.Hello{}, err
	}
	h, err := protocol.DecodeBody[protocol.Hello](e.Body)
	if err != nil {
		return h, err
	}
	err = sendFrame(c, protocol.KindResponse, e.ID, "welcome", protocol.Welcome{RoomID: roomID, RoomName: "room", ProjectRoot: "/project", ExecutionOwner: "owner", FullOwnerAccess: true, LatestSeq: high}, nil)
	return h, err
}
func TestDisconnectResendsIdenticalMutationAfterReplay(t *testing.T) {
	input, iw := io.Pipe()
	defer iw.Close()
	var out, diag bytes.Buffer
	store := &memoryCursors{}
	servers := make(chan net.Conn, 4)
	launcher := launchFunc(func(ctx context.Context, _ string) (*Connection, error) {
		a, b := net.Pipe()
		select {
		case servers <- b:
		case <-ctx.Done():
			a.Close()
			b.Close()
			return nil, ctx.Err()
		}
		return &Connection{Reader: a, Writer: a}, nil
	})
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{Launcher: launcher, Cursors: store}).Run(context.Background(), "host", input, &out, &diag)
	}()
	first := <-servers
	first.SetDeadline(time.Now().Add(3 * time.Second))
	r := protocol.NewReader(first, protocol.MaxFrameBytes)
	if _, e := welcome(first, r, "room", 0); e != nil {
		t.Fatal(e)
	}
	if e := sendFrame(first, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{}, nil); e != nil {
		t.Fatal(e)
	}
	go func() { _, _ = io.WriteString(iw, "/note human only\n") }()
	mutation, e := r.Read()
	if e != nil || mutation.Method != "note" {
		t.Fatal(mutation, e)
	}
	seq := uint64(1)
	if e = sendFrame(first, protocol.KindEvent, "", "message/accepted", room.DurableEvent{Seq: 1, Kind: "message/accepted", Payload: json.RawMessage(`{"text":"once"}`)}, &seq); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read(); e != nil {
		t.Fatal(e)
	}
	if e = sendFrame(first, protocol.KindEvent, "", "item-delta", room.TransientEvent{Revision: 1, Kind: "item-delta", ThreadID: "t", TurnID: "r", ItemID: "i", Delta: "partial"}, nil); e != nil {
		t.Fatal(e)
	}
	first.Close()
	var second net.Conn
	select {
	case second = <-servers:
	case <-time.After(3 * time.Second):
		t.Fatal("no reconnect")
	}
	second.SetDeadline(time.Now().Add(3 * time.Second))
	defer second.Close()
	r = protocol.NewReader(second, protocol.MaxFrameBytes)
	h, e := welcome(second, r, "room", 1)
	if e != nil || h.LastAppliedSeq != 1 {
		t.Fatal(h, e)
	}
	if e = sendFrame(second, protocol.KindEvent, "", "message/accepted", room.DurableEvent{Seq: 1, Kind: "message/accepted", Payload: json.RawMessage(`{"text":"once"}`)}, &seq); e != nil {
		t.Fatal(e)
	}
	if e = sendFrame(second, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{ProjectionRevision: 2}, nil); e != nil {
		t.Fatal(e)
	}
	again, e := r.Read()
	if e != nil || again.ID != mutation.ID || !bytes.Equal(again.Body, mutation.Body) {
		t.Fatal(again, e)
	}
	if e = sendFrame(second, protocol.KindResponse, again.ID, "note", protocol.Empty{}, nil); e != nil {
		t.Fatal(e)
	}
	_, _ = io.WriteString(iw, "/quit\n")
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("quit blocked")
	}
	if strings.Count(out.String(), "once") != 1 {
		t.Fatal(out.String())
	}
	if len(store.cursor.RoomID) == 0 || store.cursor.LastAppliedSeq != 1 {
		t.Fatal(store.cursor)
	}
}
func TestQuitCancelsLaunch(t *testing.T) {
	input, iw := io.Pipe()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{Cursors: &memoryCursors{}, Launcher: launchFunc(func(ctx context.Context, _ string) (*Connection, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		})}).Run(context.Background(), "host", input, io.Discard, io.Discard)
	}()
	<-entered
	_, _ = io.WriteString(iw, "/quit\n")
	iw.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("launch not cancelled")
	}
}

type fakeTimer struct{ ch chan time.Time }

func (t *fakeTimer) C() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool          { return true }

type clockEntry struct {
	d time.Duration
	t *fakeTimer
}
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	entries chan clockEntry
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fakeClock) NewTimer(d time.Duration) Timer {
	t := &fakeTimer{ch: make(chan time.Time, 1)}
	f.entries <- clockEntry{d, t}
	return t
}
func (f *fakeClock) fire(t *fakeTimer, d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	f.mu.Unlock()
	t.ch <- now
}
func TestHeartbeatAndDeadPeerTimers(t *testing.T) {
	f := &fakeClock{now: time.Unix(1, 0), entries: make(chan clockEntry, 20)}
	a, b := net.Pipe()
	defer b.Close()
	b.SetDeadline(time.Now().Add(3 * time.Second))
	input, iw := io.Pipe()
	defer iw.Close()
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{Clock: f, Cursors: &memoryCursors{}, Launcher: launchFunc(func(context.Context, string) (*Connection, error) { return &Connection{Reader: a, Writer: a}, nil })}).Run(context.Background(), "host", input, io.Discard, io.Discard)
	}()
	r := protocol.NewReader(b, protocol.MaxFrameBytes)
	if _, e := welcome(b, r, "room", 0); e != nil {
		t.Fatal(e)
	}
	_ = sendFrame(b, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{}, nil)
	go func() { _, _ = io.WriteString(iw, "/status\n") }()
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		entry := <-f.entries
		if entry.d != 20*time.Second {
			t.Fatal(entry.d)
		}
		f.fire(entry.t, 20*time.Second)
		e, err := r.Read()
		if err != nil || e.Method != "heartbeat" {
			t.Fatal(e, err)
		}
	}
	entry := <-f.entries
	f.fire(entry.t, 20*time.Second)
	if _, e := r.Read(); e == nil {
		t.Fatal("dead connection survived")
	}
	retry := <-f.entries
	if retry.d < 125*time.Millisecond || retry.d > 250*time.Millisecond {
		t.Fatal(retry.d)
	}
	_, _ = io.WriteString(iw, "/quit\n")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("backoff quit blocked")
	}
}
