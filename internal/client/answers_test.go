package client

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestAnswerTextFiltersToolsAndCommentary(t *testing.T) {
	for _, tc := range []struct{ kind, payload, want string }{
		{"item/completed", `{"payload":{"type":"agentMessage","phase":"final_answer","text":"完整回答\n代码"}}`, "完整回答\n代码"},
		{"item/completed", `{"payload":{"type":"agentMessage","text":"兼容回答"}}`, "兼容回答"},
		{"item/completed", `{"payload":{"type":"agentMessage","phase":"commentary","text":"working"}}`, ""},
		{"item/completed", `{"payload":{"type":"commandExecution","text":"secret log"}}`, ""},
		{"message/accepted", `{"payload":{"type":"agentMessage","text":"input"}}`, ""},
		{"item/completed", `{"payload":{"type":"reasoning","text":"reasoning"}}`, ""},
	} {
		got, err := answerText(room.DurableEvent{Kind: tc.kind, Payload: json.RawMessage(tc.payload)})
		if err != nil || got != tc.want {
			t.Fatalf("%s: %q %v", tc.payload, got, err)
		}
	}
}

func TestReadOnlyClientReplaysAnswersWithoutSubmitting(t *testing.T) {
	input, writer := io.Pipe()
	defer writer.Close()
	local, server := net.Pipe()
	defer server.Close()
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	var output, diag bytes.Buffer
	cursor := &ReplayCursors{}
	answers := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- New(Deps{ReadOnly: true, Cursors: cursor, Launcher: launchFunc(func(context.Context, string) (*Connection, error) {
			return &Connection{Reader: local, Writer: local}, nil
		}), OnAnswer: func(_ room.DurableEvent, text string) error { answers <- text; return nil }}).Run(context.Background(), "host", input, &output, &diag)
	}()
	reader := protocol.NewReader(server, protocol.MaxFrameBytes)
	if h, err := welcome(server, reader, "room", 1); err != nil || h.LastAppliedSeq != 0 {
		t.Fatal(h, err)
	}
	if err := sendFrame(server, protocol.KindEvent, "", "runtime-snapshot", protocol.RuntimeSnapshot{}, nil); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(writer, "DO NOT SUBMIT\n/cancel\n")
	event := room.DurableEvent{Seq: 1, Kind: "item/completed", Payload: json.RawMessage(`{"payload":{"type":"agentMessage","text":"only answer"}}`)}
	seq := uint64(1)
	for i := 0; i < 2; i++ {
		if err := sendFrame(server, protocol.KindEvent, "", "item/completed", event, &seq); err != nil {
			t.Fatal(err)
		}
	}
	envelope, err := reader.Read()
	if err != nil || envelope.Method != "ack" {
		t.Fatalf("read-only client sent %s, %v", envelope.Method, err)
	}
	_, _ = io.WriteString(writer, "/quit\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop")
	}
	if len(answers) != 1 || <-answers != "only answer" {
		t.Fatal("answer replay duplicated or missing")
	}
	if cursor.cursor.LastAppliedSeq != 1 {
		t.Fatal(cursor.cursor)
	}
}
