// Package bridge carries protocol bytes without interpreting or logging them.
package bridge

import (
	"context"
	"io"
	"net"
)

// Relay owns all three endpoints. Close must interrupt their pending I/O.
func Relay(ctx context.Context, input io.ReadCloser, output io.WriteCloser, socket net.Conn) error {
	done := make(chan error, 2)
	go func() { _, e := io.Copy(socket, input); done <- e }()
	go func() { _, e := io.Copy(output, socket); done <- e }()
	var err error
	received := 0
	select {
	case err = <-done:
		received++
	case <-ctx.Done():
		err = ctx.Err()
	}
	_ = socket.Close()
	_ = input.Close()
	_ = output.Close()
	for received < 2 {
		<-done
		received++
	}
	return err
}
