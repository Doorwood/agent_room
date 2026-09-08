package client

import (
	"agent_romm/internal/protocol"
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestWelcomeRequiresExplicitOwnerAuthority(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	b.SetDeadline(time.Now().Add(time.Second))
	in, iw := io.Pipe()
	defer iw.Close()
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{Cursors: &memoryCursors{}, Launcher: launchFunc(func(context.Context, string) (*Connection, error) { return &Connection{Reader: a, Writer: a}, nil })}).Run(context.Background(), "host", in, io.Discard, io.Discard)
	}()
	e, err := protocol.NewReader(b, protocol.MaxFrameBytes).Read()
	if err != nil {
		t.Fatal(err)
	}
	err = sendFrame(b, protocol.KindResponse, e.ID, "welcome", protocol.Welcome{RoomID: "r", RoomName: "room", ProjectRoot: "/p", ExecutionOwner: "owner"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("missing authority accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("did not reject welcome")
	}
}

func TestSavedCursorProbesRoomBeforeResume(t *testing.T) {
	for _, roomID := range []string{"old", "new"} {
		t.Run(roomID, func(t *testing.T) {
			input, iw := io.Pipe()
			defer iw.Close()
			servers := make(chan net.Conn, 3)
			store := &memoryCursors{Cursor{Target: "host", RoomID: "old", LastAppliedSeq: 7}}
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
				})}).Run(context.Background(), "host", input, &out, io.Discard)
			}()
			a := <-servers
			a.SetDeadline(time.Now().Add(3 * time.Second))
			h, e := welcome(a, protocol.NewReader(a, protocol.MaxFrameBytes), roomID, 9)
			if e != nil || h.LastAppliedSeq != 0 {
				t.Fatal(h, e)
			}
			if _, e = protocol.NewReader(a, protocol.MaxFrameBytes).Read(); e == nil {
				t.Fatal("probe not disconnected")
			}
			a.Close()
			b := <-servers
			defer b.Close()
			b.SetDeadline(time.Now().Add(3 * time.Second))
			h, e = welcome(b, protocol.NewReader(b, protocol.MaxFrameBytes), roomID, 9)
			want := uint64(7)
			if roomID == "new" {
				want = 0
			}
			if e != nil || h.LastAppliedSeq != want {
				t.Fatal(h, e)
			}
			_ = sendFrame(b, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{}, nil)
			go func() { _, _ = io.WriteString(iw, "/status\n") }()
			if _, e = protocol.NewReader(b, protocol.MaxFrameBytes).Read(); e != nil {
				t.Fatal(e)
			}
			_, _ = io.WriteString(iw, "/quit\n")
			select {
			case e = <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				t.Fatal("quit")
			}
			if store.cursor.LastAppliedSeq != want || store.cursor.RoomID != roomID {
				t.Fatal(store.cursor)
			}
			if bytes.Count(out.Bytes(), []byte("Room:")) != 1 {
				t.Fatal(out.String())
			}
		})
	}
}
func TestBackoffCapped(t *testing.T) {
	c := New(Deps{})
	for _, base := range []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second} {
		for i := 0; i < 100; i++ {
			d, e := c.jitter(base)
			if e != nil || d < base/2 || d > base || d > 10*time.Second {
				t.Fatal(d, e)
			}
		}
	}
}
