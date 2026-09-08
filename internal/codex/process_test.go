package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestExplicitProcessPipesPreserveBufferedOutputAfterWait(t *testing.T) {
	if os.Getenv("AGENT_ROMM_PIPE_HELPER") == "1" {
		_, _ = io.WriteString(os.Stdout, "final-stdout\n")
		_, _ = io.WriteString(os.Stderr, "final-stderr\n")
		os.Exit(0)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestExplicitProcessPipesPreserveBufferedOutputAfterWait$")
	command.Env = append(os.Environ(), "AGENT_ROMM_PIPE_HELPER=1")
	pipes, err := openProcessPipes(command)
	if err != nil {
		t.Fatal(err)
	}
	defer pipes.closeAll()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pipes.closeChildEnds()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	stdout, err := io.ReadAll(pipes.stdout)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(pipes.stderr)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdout) != "final-stdout\n" || string(stderr) != "final-stderr\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestOwnedChildNeverSignalsAfterExitIsObservedOrReaped(t *testing.T) {
	for _, tc := range []struct {
		name       string
		exitBefore bool
		exitOn     processSignal
		wantSignal []processSignal
	}{
		{name: "exited before stop", exitBefore: true},
		{name: "exit at TERM boundary", exitOn: processSignalTerminate, wantSignal: []processSignal{processSignalTerminate}},
		{name: "exit at KILL boundary", exitOn: processSignalKill, wantSignal: []processSignal{processSignalTerminate, processSignalKill}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := &processOwnerToken{}
			hooks := newFakeProcessHooks(tc.exitOn)
			child := newOwnedChild(owner, ProcessIdentity{Generation: "g", PID: 42, PGID: 42, StartIdentity: "start"},
				nopWriteCloser{}, nopReadCloser{}, nopReadCloser{}, hooks.hooks())
			if tc.exitBefore {
				hooks.exit(nil)
				awaitClosed(t, child.Done)
			}
			if err := stopOwnedChild(context.Background(), owner, child, 0); err != nil {
				t.Fatal(err)
			}
			if got := hooks.signalsSnapshot(); !equalSignals(got, tc.wantSignal) {
				t.Fatalf("signals=%v want=%v", got, tc.wantSignal)
			}
			if got := hooks.reapCount(); got != 1 {
				t.Fatalf("reaps=%d want=1", got)
			}
			if err := stopOwnedChild(context.Background(), owner, child, 0); err != nil {
				t.Fatal(err)
			}
			if got := hooks.signalsSnapshot(); !equalSignals(got, tc.wantSignal) {
				t.Fatalf("post-reap signals=%v want=%v", got, tc.wantSignal)
			}
		})
	}
}

func TestOwnedChildRechecksExitUnderSignalLock(t *testing.T) {
	owner := &processOwnerToken{}
	hooks := newFakeProcessHooks(0)
	hooks.exitDuringNonblockingObservation = true
	child := newOwnedChild(owner, ProcessIdentity{Generation: "g", PID: 42, PGID: 42, StartIdentity: "start"},
		nopWriteCloser{}, nopReadCloser{}, nopReadCloser{}, hooks.hooks())
	if err := stopOwnedChild(context.Background(), owner, child, 0); err != nil {
		t.Fatal(err)
	}
	if got := hooks.signalsSnapshot(); len(got) != 0 {
		t.Fatalf("signaled after exit observation: %v", got)
	}
	if got := hooks.reapCount(); got != 1 {
		t.Fatalf("reaps=%d want=1", got)
	}
}

func TestStopOwnedChildHonorsContextWhenProcessIgnoresKill(t *testing.T) {
	owner := &processOwnerToken{}
	hooks := newFakeProcessHooks(0)
	defer hooks.exit(nil)
	child := newOwnedChild(owner, ProcessIdentity{Generation: "g", PID: 42, PGID: 42, StartIdentity: "start"},
		nopWriteCloser{}, nopReadCloser{}, nopReadCloser{}, hooks.hooks())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- stopOwnedChild(ctx, owner, child, 0) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("StopCurrent err=%v want deadline", err)
		}
	case <-time.After(250 * time.Millisecond):
		hooks.exit(nil)
		<-result
		t.Fatal("StopCurrent ignored its cleanup context after SIGKILL")
	}
}

func TestStopOwnedChildRejectsForeignAndUnsafeLiveHandles(t *testing.T) {
	owner := &processOwnerToken{}
	other := &processOwnerToken{}
	hooks := newFakeProcessHooks(processSignalKill)
	child := newOwnedChild(owner, ProcessIdentity{Generation: "g", PID: 42, PGID: 42, StartIdentity: "start"},
		nopWriteCloser{}, nopReadCloser{}, nopReadCloser{}, hooks.hooks())
	copyChild := *child
	if err := stopOwnedChild(context.Background(), owner, &copyChild, 0); !errors.Is(err, ErrForeignChild) {
		t.Fatalf("copied child err=%v", err)
	}
	if err := stopOwnedChild(context.Background(), other, child, 0); !errors.Is(err, ErrForeignChild) {
		t.Fatalf("foreign owner err=%v", err)
	}

	unsafeHooks := newFakeProcessHooks(processSignalKill)
	unsafe := newOwnedChild(owner, ProcessIdentity{Generation: "g", PID: 1, PGID: 1, StartIdentity: "start"},
		nopWriteCloser{}, nopReadCloser{}, nopReadCloser{}, unsafeHooks.hooks())
	if err := stopOwnedChild(context.Background(), owner, unsafe, time.Millisecond); !errors.Is(err, ErrInvalidCurrentProcess) {
		t.Fatalf("unsafe child err=%v", err)
	}
	if got := unsafeHooks.signalsSnapshot(); len(got) != 0 {
		t.Fatalf("unsafe child signals=%v", got)
	}
	hooks.exit(nil)
	unsafeHooks.exit(nil)
}

func equalSignals(got, want []processSignal) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type fakeProcessHooks struct {
	mu                               sync.Mutex
	exited                           bool
	waitErr                          error
	reaps                            int
	signals                          []processSignal
	exitOn                           processSignal
	exitChannel                      chan struct{}
	exitOnce                         sync.Once
	exitDuringNonblockingObservation bool
}

func newFakeProcessHooks(exitOn processSignal) *fakeProcessHooks {
	return &fakeProcessHooks{exitOn: exitOn, exitChannel: make(chan struct{})}
}

func (h *fakeProcessHooks) hooks() processHooks {
	return processHooks{
		observeExit: func(nonblocking bool) (bool, error) {
			if nonblocking {
				h.mu.Lock()
				if h.exitDuringNonblockingObservation && !h.exited {
					h.exited = true
					h.exitOnce.Do(func() { close(h.exitChannel) })
				}
				exited := h.exited
				h.mu.Unlock()
				return exited, nil
			}
			<-h.exitChannel
			return true, nil
		},
		signalGroup: func(signal processSignal) error {
			h.mu.Lock()
			h.signals = append(h.signals, signal)
			shouldExit := signal == h.exitOn
			h.mu.Unlock()
			if shouldExit {
				h.exit(nil)
			}
			return nil
		},
		reap: func() error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.reaps++
			return h.waitErr
		},
	}
}

func (h *fakeProcessHooks) exit(err error) {
	h.mu.Lock()
	h.exited = true
	h.waitErr = err
	h.mu.Unlock()
	h.exitOnce.Do(func() { close(h.exitChannel) })
}

func (h *fakeProcessHooks) signalsSnapshot() []processSignal {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]processSignal(nil), h.signals...)
}

func (h *fakeProcessHooks) reapCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reaps
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

type nopReadCloser struct{}

func (nopReadCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (nopReadCloser) Close() error             { return nil }
