package codex

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDefaultJSONEscapedStringLengthMatchesEncoder(t *testing.T) {
	values := []string{
		"", "plain", `quote" slash\\`, "\b\f\n\r\t", "<>&", "\x00\x1f",
		"emoji ⚽", "\u2028\u2029", string([]byte{'a', 0xff, 'b'}),
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if got := defaultJSONEscapedStringLength(value); got != len(encoded) {
			t.Fatalf("value=%q length=%d want=%d encoding=%s", value, got, len(encoded), encoded)
		}
	}
}

func TestTextRequestPreflightPreservesExactBoundary(t *testing.T) {
	params := startTurnRequest("thread-1", string(testClientMessageID), "/srv/project", "")
	baseline, err := json.Marshal(struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params turnStartParams `json:"params"`
	}{ID: 1, Method: "turn/start", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("a", MaxJSONLLineBytes-len(baseline))
	if err := preflightStartTurnRequest("thread-1", string(testClientMessageID), "/srv/project", text); err != nil {
		t.Fatalf("exact boundary rejected: %v baseline=%d text=%d escaped=%d", err, len(baseline), len(text), defaultJSONEscapedStringLength(text))
	}
	if err := preflightStartTurnRequest("thread-1", string(testClientMessageID), "/srv/project", text+"a"); !errors.Is(err, ErrRPCMessageTooLarge) {
		t.Fatalf("over boundary err=%v", err)
	}
}

func TestRequestPreflightDoesNotMarshalDynamicStrings(t *testing.T) {
	projectRoot := "/" + strings.Repeat("<", MaxJSONLLineBytes/6+1)
	var preflightErr error
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			preflightErr = preflightStartTurnRequest("thread-1", string(testClientMessageID), projectRoot, "run")
		}
	})
	if !errors.Is(preflightErr, ErrRPCMessageTooLarge) {
		t.Fatalf("err=%v", preflightErr)
	}
	if allocated := result.AllocedBytesPerOp(); allocated > 64<<10 {
		t.Fatalf("preflight allocated %d bytes for dynamic strings", allocated)
	}
}
