package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

var (
	ErrUnsupportedPlatform   = errors.New("Codex App Server process supervision is unsupported on this platform")
	ErrInvalidProcessSpec    = errors.New("invalid Codex App Server process specification")
	ErrProcessVersion        = errors.New("Codex CLI version probe failed")
	ErrProcessStart          = errors.New("Codex App Server process start failed")
	ErrProcessIdentityRead   = errors.New("Codex App Server process identity cannot be read safely")
	ErrInvalidCurrentProcess = errors.New("unsafe current Codex App Server process identity")
	ErrForeignChild          = errors.New("Codex App Server child is not owned by this process manager")
	ErrProcessObservation    = errors.New("Codex App Server process exit cannot be observed safely")
	ErrProcessSignal         = errors.New("Codex App Server process group cannot be signaled safely")
	ErrProcessReap           = errors.New("Codex App Server process cannot be reaped safely")
)

type ProcessIdentity struct {
	Generation    string
	PID           int
	PGID          int
	StartIdentity string
}

type ProcessMatch int

const (
	ProcessUnknown ProcessMatch = iota
	ProcessAbsent
	ProcessMatches
	ProcessMismatch
)

type ProcessManager interface {
	Version(context.Context, string) (string, error)
	Start(context.Context, ProcessSpec) (*Child, error)
	Match(context.Context, ProcessIdentity) (ProcessMatch, error)
	StopCurrent(context.Context, *Child, time.Duration) error
}

type ProcessSpec struct {
	Executable string
	Args       []string
	Env        []string
	Dir        string
	Generation string
}

type Child struct {
	Identity ProcessIdentity
	Stdin    io.WriteCloser
	Stdout   io.ReadCloser
	Stderr   io.ReadCloser
	Done     <-chan struct{}
	WaitErr  func() error

	owned *ownedChildState
}

// processPipes keeps the parent endpoints outside exec.Cmd's internal pipe
// ownership. Cmd.Wait may reap immediately after exit without closing unread
// stdout/stderr bytes that the RPC and stderr drainers still need to consume.
type processPipes struct {
	stdin  *os.File
	stdout *os.File
	stderr *os.File

	childStdin  *os.File
	childStdout *os.File
	childStderr *os.File
}

func openProcessPipes(command *exec.Cmd) (*processPipes, error) {
	if command == nil {
		return nil, ErrProcessStart
	}
	childStdin, stdin, err := os.Pipe()
	if err != nil {
		return nil, ErrProcessStart
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		return nil, ErrProcessStart
	}
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		return nil, ErrProcessStart
	}
	pipes := &processPipes{
		stdin: stdin, stdout: stdout, stderr: stderr,
		childStdin: childStdin, childStdout: childStdout, childStderr: childStderr,
	}
	command.Stdin = childStdin
	command.Stdout = childStdout
	command.Stderr = childStderr
	return pipes, nil
}

func (p *processPipes) closeChildEnds() {
	if p == nil {
		return
	}
	_ = p.childStdin.Close()
	_ = p.childStdout.Close()
	_ = p.childStderr.Close()
}

func (p *processPipes) closeParentEnds() {
	if p == nil {
		return
	}
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	_ = p.stderr.Close()
}

func (p *processPipes) closeAll() {
	p.closeChildEnds()
	p.closeParentEnds()
}

// NewProcessManager returns the fail-closed process manager for the current
// platform. Only the Linux implementation can start or inspect a process.
func NewProcessManager() ProcessManager { return newPlatformProcessManager() }

// The marker keeps separately allocated ownership tokens pointer-distinct;
// pointers to zero-sized values are permitted to compare equal in Go.
type processOwnerToken struct{ marker byte }

type processSignal uint8

const (
	processSignalTerminate processSignal = 1
	processSignalKill      processSignal = 2
)

type processHooks struct {
	// observeExit waits when nonblocking is false and probes when it is true.
	// It never reaps the process.
	observeExit func(nonblocking bool) (bool, error)
	signalGroup func(processSignal) error
	reap        func() error
}

type ownedChildState struct {
	mu         sync.Mutex
	owner      *processOwnerToken
	child      *Child
	hooks      processHooks
	reaped     bool
	waitErr    error
	cleanupErr error
	done       chan struct{}
	doneOnce   sync.Once
	stdinOnce  sync.Once
}

func newOwnedChild(
	owner *processOwnerToken,
	identity ProcessIdentity,
	stdin io.WriteCloser,
	stdout io.ReadCloser,
	stderr io.ReadCloser,
	hooks processHooks,
) *Child {
	state := &ownedChildState{owner: owner, hooks: hooks, done: make(chan struct{})}
	child := &Child{Identity: identity, Stdin: stdin, Stdout: stdout, Stderr: stderr, Done: state.done, owned: state}
	state.child = child
	child.WaitErr = func() error {
		<-state.done
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.waitErr
	}
	go state.observeAndReap()
	return child
}

func (s *ownedChildState) observeAndReap() {
	exited, err := s.hooks.observeExit(false)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reaped {
		return
	}
	if err != nil || !exited {
		s.waitErr = ErrProcessObservation
		s.closeDoneLocked()
		return
	}
	s.reapLocked()
}

func (s *ownedChildState) reapLocked() {
	if s.reaped {
		return
	}
	if err := s.hooks.reap(); err != nil {
		s.waitErr = err
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			s.cleanupErr = ErrProcessReap
		}
	}
	s.reaped = true
	s.closeDoneLocked()
}

func (s *ownedChildState) closeDoneLocked() {
	s.doneOnce.Do(func() { close(s.done) })
}

func stopOwnedChild(ctx context.Context, owner *processOwnerToken, child *Child, grace time.Duration) error {
	if child == nil || child.owned == nil || child.owned.owner != owner || child.owned.child != child {
		return ErrForeignChild
	}
	state := child.owned
	state.mu.Lock()
	if state.reaped {
		err := state.cleanupErr
		state.mu.Unlock()
		return err
	}
	if child.Identity.PID <= 1 || child.Identity.PGID <= 1 {
		state.mu.Unlock()
		return ErrInvalidCurrentProcess
	}
	state.mu.Unlock()

	state.stdinOnce.Do(func() {
		if child.Stdin != nil {
			_ = child.Stdin.Close()
		}
	})
	if waitForOwnedReap(ctx, state, grace) {
		return state.reapResult()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.signalOrReap(processSignalTerminate); err != nil {
		return err
	}
	if waitForOwnedReap(ctx, state, grace) {
		return state.reapResult()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.signalOrReap(processSignalKill); err != nil {
		return err
	}
	if state.isReaped() {
		return state.reapResult()
	}
	return waitForOwnedReapAfterKill(ctx, state)
}

func waitForOwnedReapAfterKill(ctx context.Context, state *ownedChildState) error {
	const pollInterval = time.Millisecond
	for {
		if err := state.observeAndReapNonblocking(); err != nil {
			return err
		}
		if state.isReaped() {
			return state.reapResult()
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func waitForOwnedReap(ctx context.Context, state *ownedChildState, grace time.Duration) bool {
	if state.isReaped() {
		return true
	}
	if grace <= 0 {
		return false
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-state.done:
		return state.isReaped()
	case <-timer.C:
		return state.isReaped()
	case <-ctx.Done():
		return state.isReaped()
	}
}

func (s *ownedChildState) isReaped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reaped
}

func (s *ownedChildState) reapResult() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupErr
}

func (s *ownedChildState) signalOrReap(signal processSignal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reaped {
		return nil
	}
	exited, err := s.hooks.observeExit(true)
	if err != nil {
		return ErrProcessObservation
	}
	if exited {
		s.reapLocked()
		return nil
	}
	if err := s.hooks.signalGroup(signal); err != nil {
		return ErrProcessSignal
	}
	return nil
}

func (s *ownedChildState) observeAndReapNonblocking() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reaped {
		return nil
	}
	exited, err := s.hooks.observeExit(true)
	if err != nil {
		return ErrProcessObservation
	}
	if exited {
		s.reapLocked()
	}
	return nil
}
