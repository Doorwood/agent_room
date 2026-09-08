//go:build linux

package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLinuxProcessManagerPreservesBufferedOutputAfterChildExit(t *testing.T) {
	manager := newPlatformProcessManager()
	child, err := manager.Start(context.Background(), ProcessSpec{
		Executable: "/bin/sh",
		Args:       []string{"-c", "printf 'final-stdout\\n'; printf 'final-stderr\\n' >&2"},
		Env:        os.Environ(),
		Dir:        t.TempDir(),
		Generation: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, child.Done)
	stdout, stdoutErr := io.ReadAll(child.Stdout)
	stderr, stderrErr := io.ReadAll(child.Stderr)
	if stdoutErr != nil || stderrErr != nil {
		t.Fatalf("stdout err=%v stderr err=%v", stdoutErr, stderrErr)
	}
	if string(stdout) != "final-stdout\n" || string(stderr) != "final-stderr\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if err := manager.StopCurrent(context.Background(), child, 0); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxProcessManagerMatchesStableProcIdentity(t *testing.T) {
	actual, err := readLinuxProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	actual.Generation = "generation"
	manager := newPlatformProcessManager()
	match, err := manager.Match(context.Background(), actual)
	if err != nil || match != ProcessMatches {
		t.Fatalf("match=%v err=%v", match, err)
	}
	actual.StartIdentity += "0"
	match, err = manager.Match(context.Background(), actual)
	if err != nil || match != ProcessMismatch {
		t.Fatalf("changed match=%v err=%v", match, err)
	}
}

func TestLinuxProcessManagerStartsDedicatedGroupAndReapsOriginalChild(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "fakeappserver")
	build := exec.Command("go", "build", "-o", executable, "./testdata/fakeappserver")
	build.Env = append(os.Environ(), "GOCACHE=/tmp/agent-romm-gocache", "GOMODCACHE=/tmp/agent-romm-gomodcache")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakeappserver: %v\n%s", err, output)
	}
	manager := newPlatformProcessManager()
	child, err := manager.Start(context.Background(), ProcessSpec{
		Executable: executable,
		Args:       []string{"app-server", "--listen", "stdio://"},
		Env:        os.Environ(),
		Dir:        t.TempDir(),
		Generation: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.Identity.PID != child.Identity.PGID || child.Identity.PID <= 1 {
		t.Fatalf("identity=%+v", child.Identity)
	}
	if err := manager.StopCurrent(context.Background(), child, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, child.Done)
	if err := manager.StopCurrent(context.Background(), child, 0); err != nil {
		t.Fatalf("idempotent stop: %v", err)
	}
	if err := unix.Kill(child.Identity.PID, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("process still addressable after reap: %v", err)
	}
}
