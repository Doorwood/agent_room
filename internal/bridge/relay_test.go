package bridge

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestRelayDisconnect(t *testing.T) {
	in, iw := io.Pipe()
	out, ow := io.Pipe()
	defer iw.Close()
	defer out.Close()
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- Relay(context.Background(), in, ow, a) }()
	go func() { _, _ = iw.Write([]byte("exact")) }()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(b, buf); err != nil || string(buf) != "exact" {
		t.Fatal(string(buf), err)
	}
	b.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("leaked")
	}
}

func TestRelayReverseBytesAndCancelBlockedIO(t *testing.T) {
	in, iw := io.Pipe()
	defer iw.Close()
	out, ow := io.Pipe()
	defer out.Close()
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Relay(ctx, in, ow, a) }()
	go func() { _, _ = b.Write([]byte("server bytes")) }()
	buf := make([]byte, 12)
	if _, err := io.ReadFull(out, buf); err != nil || string(buf) != "server bytes" {
		t.Fatal(string(buf), err)
	}
	go func() { _, _ = b.Write([]byte("blocked output")) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation leaked copy")
	}
}
