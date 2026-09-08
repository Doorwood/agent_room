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
	"strings"
	"testing"
	"time"
)

func TestQuitAfterInputQueueOverflowCancelsLaunch(t *testing.T) {
	in, iw := io.Pipe()
	defer iw.Close()
	entered := make(chan struct{})
	done := make(chan error, 1)
	var diag bytes.Buffer
	go func() {
		done <- New(Deps{Cursors: &memoryCursors{}, Launcher: launchFunc(func(ctx context.Context, _ string) (*Connection, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		})}).Run(context.Background(), "host", in, io.Discard, &diag)
	}()
	<-entered
	go func() { _, _ = io.WriteString(iw, strings.Repeat("queued\n", 66)+"/quit\n") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diag.String(), "rejected 2 input lines") {
			t.Fatal(diag.String())
		}
	case <-time.After(time.Second):
		in.Close()
		t.Fatal("queue overflow prevented quit from reaching scanner")
	}
}

func TestSnapshotWatermarkPreventsQueuedTurnRegression(t *testing.T) {
	for _, active := range []string{"current", ""} {
		p := projection{}
		var snapshot protocol.RuntimeSnapshot
		if err := json.Unmarshal([]byte(`{"projectionRevision":9,"liveItems":[],"activeTurnId":"`+active+`","durableWatermark":12}`), &snapshot); err != nil {
			t.Fatal(err)
		}
		p.snapshot(snapshot)
		if err := p.durable(room.DurableEvent{Seq: 11, Kind: "turn/running", Payload: json.RawMessage(`{"turn_id":"obsolete"}`)}, io.Discard); err != nil {
			t.Fatal(err)
		}
		if p.active != active {
			t.Fatalf("active regressed from %q to %q", active, p.active)
		}
		if err := p.durable(room.DurableEvent{Seq: 12, Kind: "turn/completed", Payload: json.RawMessage(`{"turn_id":"current"}`)}, io.Discard); err != nil {
			t.Fatal(err)
		}
		if p.active != active {
			t.Fatal("watermarked completion cleared current snapshot turn")
		}
		for _, line := range []string{"/steer continue", "/cancel"} {
			cmd, _ := ParseCommand(line)
			env, _, err := cmd.Envelope(rand.Reader, p.active)
			if active == "" {
				if err == nil {
					t.Fatal("command accepted with completed turn")
				}
				continue
			}
			if err != nil || !bytes.Contains(env.Body, []byte(`"expectedTurnId":"current"`)) {
				t.Fatal(env, err)
			}
		}
		if err := p.durable(room.DurableEvent{Seq: 13, Kind: "turn/running", Payload: json.RawMessage(`{"turn_id":"newer"}`)}, io.Discard); err != nil {
			t.Fatal(err)
		}
		if p.active != "newer" {
			t.Fatal("new durable event ignored")
		}
	}
}

func TestOverflowQuitDuringHandshakeAndBackoff(t *testing.T) {
	for _, stage := range []string{"handshake", "backoff"} {
		t.Run(stage, func(t *testing.T) {
			in, iw := io.Pipe()
			defer iw.Close()
			a, b := net.Pipe()
			defer b.Close()
			b.SetDeadline(time.Now().Add(time.Second))
			clock := &fakeClock{now: time.Unix(1, 0), entries: make(chan clockEntry, 10)}
			var diag bytes.Buffer
			done := make(chan error, 1)
			go func() {
				done <- New(Deps{Clock: clock, Cursors: &memoryCursors{}, Launcher: launchFunc(func(context.Context, string) (*Connection, error) {
					if stage == "backoff" {
						a.Close()
						return nil, errors.New("offline")
					}
					return &Connection{Reader: a, Writer: a}, nil
				})}).Run(context.Background(), "host", in, io.Discard, &diag)
			}()
			if stage == "handshake" {
				if _, err := protocol.NewReader(b, protocol.MaxFrameBytes).Read(); err != nil {
					t.Fatal(err)
				}
			} else {
				<-clock.entries
			}
			go func() { _, _ = io.WriteString(iw, strings.Repeat("queued\n", 100)+"/quit\n") }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(diag.String(), "input queue full: rejected") {
					t.Fatal(diag.String())
				}
			case <-time.After(time.Second):
				in.Close()
				t.Fatal("quit stalled during " + stage)
			}
		})
	}
}

func TestQueuedDurableEventsPersistWithoutChangingSnapshotActive(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	b.SetDeadline(time.Now().Add(3 * time.Second))
	in, iw := io.Pipe()
	defer iw.Close()
	store := &memoryCursors{}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{Cursors: store, Launcher: launchFunc(func(context.Context, string) (*Connection, error) { return &Connection{Reader: a, Writer: a}, nil })}).Run(context.Background(), "host", in, &out, io.Discard)
	}()
	r := protocol.NewReader(b, protocol.MaxFrameBytes)
	if _, err := welcome(b, r, "room", 0); err != nil {
		t.Fatal(err)
	}
	if err := sendFrame(b, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{ActiveTurnID: "current", DurableWatermark: 2}, nil); err != nil {
		t.Fatal(err)
	}
	seq := uint64(1)
	if err := sendFrame(b, protocol.KindEvent, "", "turn/running", room.DurableEvent{Seq: 1, Kind: "turn/running", Payload: json.RawMessage(`{"turn_id":"obsolete"}`)}, &seq); err != nil {
		t.Fatal(err)
	}
	if ack, err := r.Read(); err != nil || ack.Method != "ack" {
		t.Fatal(ack, err)
	}
	go func() { _, _ = io.WriteString(iw, "/steer keep current\n") }()
	steer, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	body, err := protocol.DecodeBody[protocol.SteerRequest](steer.Body)
	if err != nil || body.ExpectedTurnID != "current" {
		t.Fatal(body, err)
	}
	seq = 2
	if err = sendFrame(b, protocol.KindEvent, "", "turn/completed", room.DurableEvent{Seq: 2, Kind: "turn/completed", Payload: json.RawMessage(`{"turn_id":"current"}`)}, &seq); err != nil {
		t.Fatal(err)
	}
	if ack, err := r.Read(); err != nil || ack.Method != "ack" {
		t.Fatal(ack, err)
	}
	go func() { _, _ = io.WriteString(iw, "/cancel\n") }()
	cancel, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	cancelBody, err := protocol.DecodeBody[protocol.CancelRequest](cancel.Body)
	if err != nil || cancelBody.ExpectedTurnID != "current" {
		t.Fatal(cancelBody, err)
	}
	_, _ = io.WriteString(iw, "/quit\n")
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("quit")
	}
	if store.cursor.LastAppliedSeq != 2 || !strings.Contains(out.String(), "obsolete") || !strings.Contains(out.String(), "turn/completed") {
		t.Fatal(store.cursor, out.String())
	}
}

func TestLiveRoomReplacementReconnectsFromZero(t *testing.T) {
	in, iw := io.Pipe()
	defer iw.Close()
	servers := make(chan net.Conn, 4)
	store := &memoryCursors{}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{Cursors: store, Launcher: launchFunc(func(ctx context.Context, _ string) (*Connection, error) {
			a, b := net.Pipe()
			select {
			case servers <- b:
			case <-ctx.Done():
				a.Close()
				b.Close()
				return nil, ctx.Err()
			}
			return &Connection{Reader: a, Writer: a}, nil
		})}).Run(context.Background(), "host", in, &out, io.Discard)
	}()
	get := func() net.Conn {
		t.Helper()
		select {
		case c := <-servers:
			c.SetDeadline(time.Now().Add(2 * time.Second))
			t.Cleanup(func() { c.Close() })
			return c
		case <-time.After(2 * time.Second):
			t.Fatal("no connection")
			return nil
		}
	}
	a := get()
	r := protocol.NewReader(a, protocol.MaxFrameBytes)
	if _, e := welcome(a, r, "old", 0); e != nil {
		t.Fatal(e)
	}
	_ = sendFrame(a, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{}, nil)
	seq := uint64(3)
	_ = sendFrame(a, protocol.KindEvent, "", "message/accepted", room.DurableEvent{Seq: 3, Kind: "message/accepted", Payload: json.RawMessage(`{"text":"old"}`)}, &seq)
	if _, e := r.Read(); e != nil {
		t.Fatal(e)
	}
	a.Close()
	b := get()
	r = protocol.NewReader(b, protocol.MaxFrameBytes)
	h, e := welcome(b, r, "new", 5)
	if e != nil || h.LastAppliedSeq != 3 {
		t.Fatal(h, e)
	}
	if _, e = r.Read(); e == nil {
		t.Fatal("wrong-room session remained open")
	}
	c := get()
	r = protocol.NewReader(c, protocol.MaxFrameBytes)
	h, e = welcome(c, r, "new", 5)
	if e != nil || h.LastAppliedSeq != 0 {
		t.Fatal(h, e)
	}
	seq = 1
	_ = sendFrame(c, protocol.KindEvent, "", "message/accepted", room.DurableEvent{Seq: 1, Kind: "message/accepted", Payload: json.RawMessage(`{"text":"new prefix"}`)}, &seq)
	_ = sendFrame(c, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{}, nil)
	go func() { _, _ = io.WriteString(iw, "/status\n") }()
	if _, e = r.Read(); e != nil {
		t.Fatal(e)
	}
	_, _ = io.WriteString(iw, "/quit\n")
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("quit blocked")
	}
	if strings.Count(out.String(), "new prefix") != 1 || store.cursor.RoomID != "new" || store.cursor.LastAppliedSeq != 1 {
		t.Fatal(out.String(), store.cursor)
	}
}
