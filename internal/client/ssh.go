package client

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unicode"
)

type Launcher interface {
	Start(context.Context, string) (*Connection, error)
}

// Connection owns its streams. Close must interrupt pending reads and writes.
type Connection struct {
	Reader    io.ReadCloser
	Writer    io.WriteCloser
	CloseFunc func() error
	WaitFunc  func() error
	once      sync.Once
	closeErr  error
}

func (c *Connection) Close() error {
	c.once.Do(func() {
		_ = c.Reader.Close()
		_ = c.Writer.Close()
		if c.CloseFunc != nil {
			c.closeErr = c.CloseFunc()
		}
	})
	return c.closeErr
}
func (c *Connection) Wait() error {
	if c.WaitFunc != nil {
		return c.WaitFunc()
	}
	return nil
}

type SSHLauncher struct{ Stderr io.Writer }

func ValidateTarget(target string) error {
	if strings.TrimSpace(target) == "" || strings.HasPrefix(target, "-") {
		return errors.New("invalid SSH target")
	}
	for _, r := range target {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return errors.New("invalid SSH target")
		}
	}
	return nil
}
func (l SSHLauncher) Start(ctx context.Context, target string) (*Connection, error) {
	if e := ValidateTarget(target); e != nil {
		return nil, e
	}
	cmd := sshCommand(ctx, target)
	cmd.Stderr = l.Stderr
	return startCommand(cmd)
}

func sshCommand(ctx context.Context, target string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh", "-T", target, "agent_romm bridge")
}

// The owned pipe permits Wait to reap the child without truncating stdout.
func startCommand(cmd *exec.Cmd) (*Connection, error) {
	in, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	out, childOut, e := os.Pipe()
	if e != nil {
		in.Close()
		return nil, e
	}
	cmd.Stdout = childOut
	if e = cmd.Start(); e != nil {
		in.Close()
		out.Close()
		childOut.Close()
		return nil, e
	}
	childOut.Close()
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	return &Connection{Reader: out, Writer: in, CloseFunc: func() error {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
		}
		<-done
		return nil
	}, WaitFunc: func() error { <-done; return waitErr }}, nil
}
