package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
)

type persistedService struct {
	*fixture
	db *store.Store
}

func (p *persistedService) Note(ctx context.Context, a room.Actor, in room.SubmitInput) (room.Acceptance, error) {
	accepted, err := p.db.AppendNote(ctx, "r", a, in)
	if err == nil {
		p.hub.PublishDurable(accepted.Event)
	}
	return accepted, err
}

type rangedEvents struct {
	db     *store.Store
	pages  int
	atHigh func()
}

func (r *rangedEvents) LatestSeq(ctx context.Context, id room.RoomID) (room.Seq, error) {
	high, err := r.db.LatestSeq(ctx, id)
	if r.atHigh != nil {
		r.atHigh()
	}
	return high, err
}
func (r *rangedEvents) Events(ctx context.Context, id room.RoomID, after, through room.Seq, limit int) ([]room.DurableEvent, error) {
	r.pages++
	return r.db.Events(ctx, id, after, through, limit)
}

func TestMutationBudgetPersistedLiveAndPaginatedReplay(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "room.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.InitializeRoom(ctx, store.RoomSeed{ID: "r", DisplayName: "room", HostID: "host", ProjectRoot: "/tmp", ExecutionOwnerUID: 1002, Members: []room.Member{{UID: 1002, Name: "bob"}}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{}
	svc := &persistedService{fixture: f, db: db}
	ranges := &rangedEvents{db: db}
	_, path := startServer(t, 1002, f, func(d *Dependencies) { d.Coordinator = svc; d.Events = ranges; d.Connections = db; d.Members = db })
	c := dial(t, path)
	hello(t, c)
	// Exactly the application budget, including JSON field names and the ID.
	base, _ := json.Marshal(protocol.SubmitRequest{ClientMessageID: reqID, Text: ""})
	near := protocol.SubmitRequest{ClientMessageID: reqID, Text: strings.Repeat("x", MaximumMutationBodyBytes-len(base))}
	request(t, c, "note", near)
	response, event := read(t, c), read(t, c)
	if response.Kind != protocol.KindResponse || event.Seq == nil || *event.Seq != 1 {
		t.Fatal(response, event)
	}
	high, err := db.LatestSeq(ctx, "r")
	if err != nil || high != 1 {
		t.Fatal(high, err)
	}
	// Rejected requests leave no event, message, or idempotency record: reusing
	// the rejected ID below must create a fresh acceptance at sequence 2.
	over := near
	over.ClientMessageID = fmt.Sprintf("%032x", 2)
	over.Text += "x"
	request(t, c, "note", over)
	assertWireError(t, read(t, c), "message-too-large")
	if high, err = db.LatestSeq(ctx, "r"); err != nil || high != 1 {
		t.Fatal(high, err)
	}
	over.Text = "valid retry"
	request(t, c, "note", over)
	got := read(t, c)
	var acceptance room.Acceptance
	if err = json.Unmarshal(got.Body, &acceptance); err != nil || acceptance.Duplicate || acceptance.Seq != 2 {
		t.Fatal(acceptance, err)
	}
	read(t, c)
	c.Close()
	for i := 3; i <= 130; i++ {
		if _, err = db.AppendNote(ctx, "r", room.Actor{UID: 1002, Name: "bob"}, room.SubmitInput{ClientMessageID: room.ClientMessageID(fmt.Sprintf("%032x", i)), Text: "history"}); err != nil {
			t.Fatal(err)
		}
	}
	// Publish a new durable event after the replay high-water read; it must be
	// queued while two historical pages and the replacement snapshot are sent.
	ranges.atHigh = func() {
		accepted, err := db.AppendNote(ctx, "r", room.Actor{UID: 1002, Name: "bob"}, room.SubmitInput{ClientMessageID: room.ClientMessageID(fmt.Sprintf("%032x", 131)), Text: "handoff"})
		if err != nil {
			panic(err)
		}
		f.hub.PublishDurable(accepted.Event)
	}
	reconnect := dial(t, path)
	request(t, reconnect, "hello", protocol.Hello{MinVersion: 1, MaxVersion: 1})
	read(t, reconnect)
	for seq := uint64(1); seq <= 130; seq++ {
		e := read(t, reconnect)
		if e.Seq == nil || *e.Seq != seq {
			t.Fatalf("wanted %d, got %#v", seq, e)
		}
	}
	if e := read(t, reconnect); e.Method != "runtime-snapshot" {
		t.Fatal(e)
	}
	if e := read(t, reconnect); e.Seq == nil || *e.Seq != 131 {
		t.Fatal(e)
	}
	// Reading all frames synchronizes with the preceding replay calls.
	if ranges.pages != 2 {
		t.Fatal(ranges.pages)
	}
}

func assertWireError(t *testing.T, e protocol.Envelope, code string) {
	t.Helper()
	var body WireError
	if err := json.Unmarshal(e.Body, &body); err != nil || e.Kind != protocol.KindError || e.ID != reqID || body.Code != code {
		t.Fatal(e, body, err)
	}
}

type oversizedView struct{}

func (oversizedView) Status(context.Context) ([]byte, error) { return nil, nil }
func (oversizedView) Diff(context.Context) ([]byte, error) {
	return []byte(strings.Repeat("x", int(protocol.MaxFrameBytes))), nil
}
func TestOversizedQueriesReturnErrorAndKeepSessionUsable(t *testing.T) {
	// This test measures encoded-size rejection, not idle expiration. Race
	// instrumentation of repeated 8 MiB marshals can exceed a one-second idle.
	f := &fixture{timeout: 10 * time.Second}
	_, path := startServer(t, 1002, f, func(d *Dependencies) { d.ProjectView = oversizedView{} })
	c := dial(t, path)
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	hello(t, c)
	f.snapshot = func() room.Snapshot {
		return room.Snapshot{LiveItems: []room.LiveItemSnapshot{{Partial: strings.Repeat("x", int(protocol.MaxFrameBytes))}}}
	}
	for _, method := range []string{"diff", "queue", "status"} {
		request(t, c, method, protocol.Empty{})
		assertWireError(t, read(t, c), "response-too-large")
		request(t, c, "heartbeat", protocol.Heartbeat{UnixMilli: 42})
		if got := read(t, c); got.Kind != protocol.KindResponse || got.Method != "heartbeat" {
			t.Fatal(got)
		}
	}
}
func TestMarshalFailureBeforeWriteReturnsSafeResponse(t *testing.T) {
	f := &fixture{}
	_, path := startServer(t, 1002, f)
	c := dial(t, path)
	// Direct writer test uses a second real socket pair independent of serving.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(filepath.Dir(path), "writer"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peer, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	accepted, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	c.Close()
	done := make(chan error, 1)
	go func() {
		w := sessionWriter{accepted, protocol.NewWriter(accepted), time.Second}
		done <- w.response(reqID, "status", json.RawMessage(`invalid SECRET`))
	}()
	assertWireError(t, read(t, peer), "response-too-large")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEveryTextMutationRejectsOverBudgetBeforeDispatch(t *testing.T) {
	f := &fixture{}
	_, path := startServer(t, 1002, f)
	c := dial(t, path)
	hello(t, c)
	large := strings.Repeat("x", MaximumMutationBodyBytes)
	for _, test := range []struct {
		method string
		body   any
	}{
		{"submit", protocol.SubmitRequest{ClientMessageID: reqID, Text: large}},
		{"note", protocol.SubmitRequest{ClientMessageID: reqID, Text: large}},
		{"steer", protocol.SteerRequest{ClientMessageID: reqID, ExpectedTurnID: "turn-1", Text: large}},
		{"cancel", protocol.CancelRequest{ClientMessageID: reqID, ExpectedTurnID: large}},
		{"recover", protocol.RecoverRequest{ClientMessageID: reqID, TargetMessageID: fmt.Sprintf("%032x", 2), Action: "continue", ReplacementMessageID: fmt.Sprintf("%032x", 3), Instruction: large}},
		{"resolve", protocol.RecoverRequest{ClientMessageID: reqID, TargetMessageID: fmt.Sprintf("%032x", 2), Action: "continue", ReplacementMessageID: fmt.Sprintf("%032x", 3), Instruction: large}},
	} {
		request(t, c, test.method, test.body)
		assertWireError(t, read(t, c), "message-too-large")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 0 {
		t.Fatal("mutation dispatched")
	}
}
