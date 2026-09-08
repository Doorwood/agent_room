package codex

import (
	"context"
	"errors"
	"testing"
)

func TestMissingThreadClassificationRequiresExactResponse(t *testing.T) {
	for _, tc := range []struct {
		err     error
		missing bool
	}{
		{&RPCError{Code: -32600, Message: "thread not loaded: thread-1"}, true},
		{&CallError{Err: &RPCError{Code: -32600, Message: "thread not loaded: thread-1"}}, true},
		{&RPCError{Code: -32600, Message: "thread not loaded: thread-2"}, false},
		{&RPCError{Code: -32603, Message: "thread not loaded: thread-1"}, false},
		{&RPCError{Code: -32600, Message: "backend unavailable"}, false},
		{context.DeadlineExceeded, false},
	} {
		got := readThreadError(tc.err, "thread-1")
		if errors.Is(got, ErrThreadNotFound) != tc.missing {
			t.Fatal(tc.err, got)
		}
	}
}
