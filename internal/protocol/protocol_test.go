package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"

	"agent_romm/internal/room"
)

const validID = "00000000000000000000000000000001"

func TestReaderRejectsFrameBoundaries(t *testing.T) {
	frame := func(length uint32, body []byte) []byte {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], length)
		return append(prefix[:], body...)
	}
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"empty", frame(0, nil), ErrEmptyFrame},
		{"max unsigned", frame(^uint32(0), nil), ErrFrameTooLarge},
		{"eof prefix", []byte{0, 0, 0}, io.ErrUnexpectedEOF},
		{"eof body", frame(3, []byte{'{'}), io.ErrUnexpectedEOF},
		{"invalid utf8", frame(1, []byte{0xff}), ErrInvalidUTF8},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewReader(bytes.NewReader(test.data), MaxFrameBytes).Read()
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}

	exactBody := []byte(`{"version":1,"kind":"request","id":"` + validID + `","method":"status","body":{}}`)
	exactBody = append(exactBody, bytes.Repeat([]byte{' '}, int(MaxFrameBytes)-len(exactBody))...)
	got, err := NewReader(bytes.NewReader(frame(MaxFrameBytes, exactBody)), MaxFrameBytes).Read()
	if err != nil {
		t.Fatalf("exactly max valid frame rejected: %v", err)
	}
	if got.ID != validID {
		t.Fatalf("got %#v", got)
	}
}

func TestReaderRejectsInvalidEnvelopeEncoding(t *testing.T) {
	frame := func(body string) []byte {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
		return append(prefix[:], body...)
	}
	validBody := `{"version":1,"kind":"request","id":"` + validID + `","method":"status","body":{}}`
	cases := []struct {
		name string
		body string
		want error
	}{
		{"malformed", `{"version":`, nil},
		{"duplicate nested", `{"version":1,"kind":"request","id":"` + validID + `","method":"status","body":{"nested":{"x":1,"x":2}}}`, ErrDuplicateKey},
		{"trailing", validBody + ` {}`, ErrTrailingJSON},
		{"unknown", `{"version":1,"kind":"request","id":"` + validID + `","method":"status","body":{},"actor":"alice"}`, nil},
		{"invalid kind", `{"version":1,"kind":"bogus","id":"` + validID + `","method":"status","body":{}}`, ErrInvalidKind},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewReader(bytes.NewReader(frame(test.body)), MaxFrameBytes).Read()
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
			if test.want == nil && err == nil {
				t.Fatal("expected decoding error")
			}
		})
	}
}

func TestRequestAndResponseIDsMustBeLowercaseHex(t *testing.T) {
	for _, id := range []string{
		"",
		"0000000000000000000000000000000",
		"000000000000000000000000000000001",
		"0000000000000000000000000000000A",
		"0000000000000000000000000000000g",
		"0000000000000000000000000000000\n",
	} {
		for _, kind := range []Kind{KindRequest, KindResponse} {
			envelope := Envelope{Version: Version, Kind: kind, ID: id, Method: "status", Body: json.RawMessage(`{}`)}
			if err := envelope.Validate(); !errors.Is(err, ErrInvalidID) {
				t.Fatalf("kind=%s id=%q got %v", kind, id, err)
			}
		}
	}
}

func TestDecodeBodyIsStrict(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"name":"ok","name":"again"}`),
		json.RawMessage(`{"name":"ok","unknown":true}`),
		json.RawMessage(`{"name":"ok"} {}`),
		json.RawMessage{0xff},
	} {
		if _, err := DecodeBody[body](raw); err == nil {
			t.Fatalf("expected strict decode failure for %q", raw)
		}
	}
	got, err := DecodeBody[body](json.RawMessage(`{"name":"ok"}`))
	if err != nil || got.Name != "ok" {
		t.Fatalf("got %#v, %v", got, err)
	}
	if _, err := DecodeBody[SubmitRequest](json.RawMessage(`{"clientMessageId":"bad","text":"work"}`)); err == nil {
		t.Fatal("accepted malformed client message ID")
	}
}

func TestDecoderRejectsCaseInsensitiveWireKeyAliases(t *testing.T) {
	envelope := `{"Version":1,"kind":"request","id":"` + validID + `","method":"status","body":{}}`
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(envelope)))
	if _, err := NewReader(bytes.NewReader(append(prefix[:], envelope...)), MaxFrameBytes).Read(); err == nil {
		t.Fatal("accepted incorrectly cased envelope key")
	}

	if _, err := DecodeBody[SubmitRequest](json.RawMessage(`{"ClientMessageId":"` + validID + `","text":"work"}`)); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("case-variant payload key got %v, want unknown field", err)
	}
	if _, err := DecodeBody[SubmitRequest](json.RawMessage(`{"clientMessageId":"` + validID + `","ClientMessageId":"00000000000000000000000000000002","text":"work"}`)); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("case-alias pair got %v, want duplicate key", err)
	}
	if _, err := DecodeBody[SubmitRequest](json.RawMessage(`{"ClientMessageId":"` + validID + `","clientMessageId":"00000000000000000000000000000002","text":"work"}`)); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("reversed case-alias pair got %v, want duplicate key", err)
	}
}

func TestDecoderRejectsCaseAliasInNestedTypedPayload(t *testing.T) {
	_, err := DecodeBody[RuntimeSnapshot](json.RawMessage(`{
		"projectionRevision":1,
		"liveItems":[{"threadId":"thread-1","ThreadId":"thread-2","turnId":"turn-1","itemId":"item-1","partial":"working"}]
	}`))
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("got %v, want duplicate key", err)
	}
}

func TestDecoderAllowsNullForOptionalEnvelopeAndContainers(t *testing.T) {
	envelope := `{"version":1,"kind":"request","id":"` + validID + `","method":"status","requires":null,"body":{}}`
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(envelope)))
	gotEnvelope, err := NewReader(bytes.NewReader(append(prefix[:], envelope...)), MaxFrameBytes).Read()
	if err != nil {
		t.Fatalf("requires:null rejected: %v", err)
	}
	if gotEnvelope.Requires != nil {
		t.Fatalf("requires=%#v, want nil", gotEnvelope.Requires)
	}

	pointer, err := DecodeBody[*SubmitRequest](json.RawMessage(`null`))
	if err != nil || pointer != nil {
		t.Fatalf("pointer=%#v err=%v", pointer, err)
	}
	slice, err := DecodeBody[[]SubmitRequest](json.RawMessage(`null`))
	if err != nil || slice != nil {
		t.Fatalf("slice=%#v err=%v", slice, err)
	}
	mapValue, err := DecodeBody[map[string]SubmitRequest](json.RawMessage(`null`))
	if err != nil || mapValue != nil {
		t.Fatalf("map=%#v err=%v", mapValue, err)
	}
	array, err := DecodeBody[[1]SubmitRequest](json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("array=%#v err=%v", array, err)
	}
	if _, err := DecodeBody[Hello](json.RawMessage(`null`)); !errors.Is(err, ErrInvalidHello) {
		t.Fatalf("null hello got %v, want validation error", err)
	}
}

func TestSessionRequiresHelloFirst(t *testing.T) {
	state := NewHandshakeState([]string{"resume-v1"})
	err := state.Accept(Envelope{Version: Version, Kind: KindRequest, ID: validID, Method: "submit", Body: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrHandshakeRequired) {
		t.Fatalf("got %v", err)
	}
}

func TestHandshakeRejectsUnknownRequiredCapability(t *testing.T) {
	state := NewHandshakeState([]string{"resume-v1"})
	err := state.Accept(Envelope{Version: Version, Kind: KindRequest, ID: validID, Method: "hello", Requires: []string{"root-shell-v9"}, Body: json.RawMessage(`{"minVersion":1,"maxVersion":1,"lastAppliedSeq":0}`)})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("got %v", err)
	}
}

func TestHandshakeAcceptsCompatibleHelloOnce(t *testing.T) {
	state := NewHandshakeState([]string{"resume-v1"})
	hello := Envelope{Version: Version, Kind: KindRequest, ID: validID, Method: "hello", Requires: []string{"resume-v1"}, Body: json.RawMessage(`{"minVersion":1,"maxVersion":1,"lastAppliedSeq":9}`)}
	if err := state.Accept(hello); err != nil {
		t.Fatal(err)
	}
	if !state.Complete() {
		t.Fatal("handshake was not marked complete")
	}
	if err := state.Accept(hello); !errors.Is(err, ErrHandshakeAlreadyComplete) {
		t.Fatalf("got %v", err)
	}
}

func TestClientRequestsValidateWithoutIdentityFields(t *testing.T) {
	if err := (SubmitRequest{ClientMessageID: validID, Text: "work"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (SteerRequest{ClientMessageID: validID, ExpectedTurnID: "turn-1", Text: "focus"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (CancelRequest{ClientMessageID: validID, ExpectedTurnID: "turn-1"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (RecoverRequest{ClientMessageID: validID, TargetMessageID: "00000000000000000000000000000002", Action: string(room.RecoveryContinue), ReplacementMessageID: "00000000000000000000000000000003", Instruction: "try safely"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (RecoverRequest{ClientMessageID: validID, TargetMessageID: "00000000000000000000000000000002", Action: "continue", ReplacementMessageID: validID, Instruction: "same ID"}).Validate(); err == nil {
		t.Fatal("accepted duplicate replacement ID")
	}
}

type shortWriter struct {
	mu           sync.Mutex
	buf          bytes.Buffer
	max          int
	firstWrite   sync.Once
	firstEntered chan struct{}
	releaseFirst <-chan struct{}
}

func (writer *shortWriter) Write(body []byte) (int, error) {
	shouldGate := false
	writer.firstWrite.Do(func() { shouldGate = true })
	writer.mu.Lock()
	if len(body) > writer.max {
		body = body[:writer.max]
	}
	count, err := writer.buf.Write(body)
	writer.mu.Unlock()
	if shouldGate {
		close(writer.firstEntered)
		<-writer.releaseFirst
	}
	return count, err
}

func TestWriterKeepsConcurrentFramesIntact(t *testing.T) {
	releaseFirst := make(chan struct{})
	destination := &shortWriter{max: 1, firstEntered: make(chan struct{}), releaseFirst: releaseFirst}
	writer := NewWriter(destination)
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		if err := writer.Write(Envelope{Version: Version, Kind: KindResponse, ID: validID, Method: "status", Body: json.RawMessage(`{"state":"ready"}`)}); err != nil {
			t.Errorf("response write: %v", err)
		}
	}()
	<-destination.firstEntered
	group.Add(1)
	go func() {
		defer group.Done()
		if err := writer.Write(Envelope{Version: Version, Kind: KindEvent, Method: "heartbeat", Body: json.RawMessage(`{"unixMilli":1}`)}); err != nil {
			t.Errorf("event write: %v", err)
		}
	}()
	close(releaseFirst)
	group.Wait()

	reader := NewReader(bytes.NewReader(destination.buf.Bytes()), MaxFrameBytes)
	first, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind == second.Kind || (first.Kind != KindResponse && second.Kind != KindResponse) || (first.Kind != KindEvent && second.Kind != KindEvent) {
		t.Fatalf("unexpected kinds %q, %q", first.Kind, second.Kind)
	}
	if _, err := reader.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing frame: %v", err)
	}
}
