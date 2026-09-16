//go:build darwin

package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinIsolatedChildOutputAndOwnership(t *testing.T) {
	manager := newIsolatedProcessManager()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child, err := manager.Start(ctx, ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "printf final; printf diagnostic >&2"}, Env: os.Environ(), Dir: t.TempDir(), Generation: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.StopCurrent(context.Background(), child, 0) })
	select {
	case <-child.Done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer child.Stdout.Close()
	defer child.Stderr.Close()
	out, err := io.ReadAll(child.Stdout)
	if err != nil || string(out) != "final" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	diagnostic, err := io.ReadAll(child.Stderr)
	if err != nil || string(diagnostic) != "diagnostic" {
		t.Fatalf("stderr=%q err=%v", diagnostic, err)
	}
	if err := newIsolatedProcessManager().StopCurrent(ctx, child, 0); !errors.Is(err, ErrForeignChild) {
		t.Fatalf("foreign stop: %v", err)
	}
	if err := manager.StopCurrent(ctx, child, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Match(ctx, child.Identity); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("persistent adoption enabled: %v", err)
	}
}

func TestDarwinIsolatedChildCancellationReaps(t *testing.T) {
	manager := newIsolatedProcessManager()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child, err := manager.Start(ctx, ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "trap '' TERM; printf ready; while :; do :; done"}, Env: os.Environ(), Dir: t.TempDir(), Generation: "cancel-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.StopCurrent(context.Background(), child, 0) })
	defer child.Stdout.Close()
	defer child.Stderr.Close()
	ready := make([]byte, 5)
	if _, err := io.ReadFull(child.Stdout, ready); err != nil {
		t.Fatal(err)
	}
	if child.Identity.PID != child.Identity.PGID {
		t.Fatal("missing dedicated process group")
	}
	if err := manager.StopCurrent(ctx, child, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(child.Identity.PID, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("child not reaped: %v", err)
	}
	if err := manager.StopCurrent(ctx, child, 0); err != nil {
		t.Fatal(err)
	}
}
