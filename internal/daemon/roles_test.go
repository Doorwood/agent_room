package daemon

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

type roleFixture struct {
	*fixture
	role string
}

func (f roleFixture) MemberRole(context.Context, room.RoomID, room.UID) (string, error) {
	return f.role, nil
}
func TestReadOnlyRolesRejectAllMainMutations(t *testing.T) {
	for _, role := range []string{"visitor", "asker"} {
		for _, method := range []string{"submit", "note", "steer", "cancel", "recover", "resolve", "diff", "queue", "status"} {
			t.Run(role+"/"+method, func(t *testing.T) {
				f := &fixture{}
				s := &Server{deps: Dependencies{Members: roleFixture{fixture: f, role: role}}}
				a, b := net.Pipe()
				defer a.Close()
				defer b.Close()
				b.SetReadDeadline(time.Now().Add(time.Second))
				done := make(chan error, 1)
				go func() {
					_, err := s.dispatch(context.Background(), sessionWriter{a, protocol.NewWriter(a), time.Second}, room.Actor{UID: 1002, Name: "bob"}, "", protocol.Envelope{Version: 1, Kind: protocol.KindRequest, ID: strings.Repeat("a", 32), Method: method, Body: json.RawMessage(`{}`)})
					done <- err
				}()
				env, err := protocol.NewReader(b, protocol.MaxFrameBytes).Read()
				if err != nil || env.Kind != protocol.KindError {
					t.Fatal(env, err)
				}
				if err = <-done; err != nil {
					t.Fatal(err)
				}
				if f.calls != 0 {
					t.Fatal("coordinator called")
				}
			})
		}
	}
}
