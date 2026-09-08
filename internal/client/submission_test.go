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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, writer := io.Pipe()
	defer writer.Close()
	local, server := net.Pipe()
	defer server.Close()
	server.SetDeadline(time.Now().Add(4 * time.Second))
	submissions := make(chan Submission)
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{Cursors: &ReplayCursors{}, Submissions: submissions, Launcher: launchFunc(func(context.Context, string) (*Connection, error) {
			return &Connection{Reader: local, Writer: local}, nil
		})}).Run(ctx, "host", input, io.Discard, io.Discard)
	}()
	reader := protocol.NewReader(server, protocol.MaxFrameBytes)
	if _, err := welcome(server, reader, "room", 0); err != nil {
		t.Fatal(err)
	}
	if err := sendFrame(server, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{}, nil); err != nil {
		t.Fatal(err)
	}
	submission := Submission{Context: ctx, ID: "0123456789abcdef0123456789abcdef", Text: "one\ntwo", Result: make(chan error, 1)}
	submissions <- submission
	env, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	var body protocol.SubmitRequest
	if err := json.Unmarshal(env.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.ClientMessageID != submission.ID || body.Text != submission.Text || env.Method != "submit" {
		t.Fatal(env, body)
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
	if err := sendFrame(server, protocol.KindResponse, env.ID, "submit", protocol.Empty{}, nil); err != nil {
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
