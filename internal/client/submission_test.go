package client

import (
	"agent_romm/internal/protocol"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestBrowserSubmissionUsesStableIDAndWaitsForHostAck(t *testing.T) {
	for _, method := range []string{"submit", "cancel"} {
		t.Run(method, func(t *testing.T) { testBrowserSubmissionAck(t, method) })
	}
}
func testBrowserSubmissionAck(t *testing.T, method string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, writer := io.Pipe()
	defer writer.Close()
	local, server := net.Pipe()
	defer server.Close()
	server.SetDeadline(time.Now().Add(4 * time.Second))
	submissions := make(chan Submission)
	activeTurns := make(chan string, 8)
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{Cursors: &ReplayCursors{}, OnActiveTurn: func(turn string) { activeTurns <- turn }, Submissions: submissions, Launcher: launchFunc(func(context.Context, string) (*Connection, error) {
			return &Connection{Reader: local, Writer: local}, nil
		})}).Run(ctx, "host", input, io.Discard, io.Discard)
	}()
	reader := protocol.NewReader(server, protocol.MaxFrameBytes)
	if _, err := welcome(server, reader, "room", 0); err != nil {
		t.Fatal(err)
	}
	if err := sendFrame(server, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{ActiveTurnID: "turn-active"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case turn := <-activeTurns:
		if turn != "turn-active" {
			t.Fatal(turn)
		}
	case <-time.After(time.Second):
		t.Fatal("missing active turn snapshot")
	}
	submission := Submission{Method: method, ExpectedTurnID: "turn-active", Context: ctx, ID: "0123456789abcdef0123456789abcdef", Text: "one\ntwo", Result: make(chan error, 1)}
	submissions <- submission
	env, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if env.Method != method {
		t.Fatal(env)
	}
	if method == "submit" {
		var body protocol.SubmitRequest
		if err := json.Unmarshal(env.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.ClientMessageID != submission.ID || body.Text != submission.Text {
			t.Fatal(body)
		}
	} else {
		var body protocol.CancelRequest
		if err := json.Unmarshal(env.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.ClientMessageID != submission.ID || body.ExpectedTurnID != "turn-active" {
			t.Fatal(body)
		}
	}
	duplicate := submission
	duplicate.Result = make(chan error, 1)
	submissions <- duplicate
	retried, err := reader.Read()
	if err != nil || retried.ID != env.ID {
		t.Fatal(retried, err)
	}
	select {
	case <-submission.Result:
		t.Fatal("acknowledged before host")
	default:
	}
	if err := sendFrame(server, protocol.KindResponse, env.ID, method, protocol.Empty{}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-submission.Result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing host result")
	}
	select {
	case err := <-duplicate.Result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("duplicate waiter lost acknowledgement")
	}
	again, _ := submission.envelope()
	if env.ID != again.ID {
		t.Fatal("retry changed ID")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client leaked")
	}
}
