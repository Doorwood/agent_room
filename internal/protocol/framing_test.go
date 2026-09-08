package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

type oneByteReader struct{ r io.Reader }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.r.Read(p)
}

func mustFrame(t *testing.T, envelope Envelope) []byte {
	t.Helper()
	var destination bytes.Buffer
	if err := NewWriter(&destination).Write(envelope); err != nil {
		t.Fatal(err)
	}
	return destination.Bytes()
}

func TestReaderHandlesFragmentedAndAdjacentFrames(t *testing.T) {
	first := mustFrame(t, Envelope{Version: 1, Kind: KindRequest, ID: "00000000000000000000000000000001", Method: "hello", Body: json.RawMessage(`{"minVersion":1,"maxVersion":1,"lastAppliedSeq":0}`)})
	second := mustFrame(t, Envelope{Version: 1, Kind: KindRequest, ID: "00000000000000000000000000000002", Method: "status", Body: json.RawMessage(`{}`)})
	r := NewReader(&oneByteReader{r: bytes.NewReader(append(first, second...))}, MaxFrameBytes)
	if got, err := r.Read(); err != nil || got.ID != "00000000000000000000000000000001" {
		t.Fatalf("first: %#v %v", got, err)
	}
	if got, err := r.Read(); err != nil || got.ID != "00000000000000000000000000000002" {
		t.Fatalf("second: %#v %v", got, err)
	}
}

func TestReaderRejectsOversizeBeforeBodyAllocation(t *testing.T) {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], MaxFrameBytes+1)
	_, err := NewReader(bytes.NewReader(prefix[:]), MaxFrameBytes).Read()
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
}
