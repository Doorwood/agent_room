package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent_romm/internal/room"
)

const (
	processGenerationBytes = 16
	stderrRingBytes        = 64 << 10
	defaultStopGrace       = 2 * time.Second
	defaultCleanupTimeout  = 10 * time.Second
)

var (
	ErrNilProcessManager        = errors.New("Codex process manager is required")
	ErrInvalidVersionPolicy     = errors.New("invalid Codex version policy")
	ErrUnsupportedSchema        = errors.New("unsupported Codex App Server schema")
	ErrGenerationUnavailable    = errors.New("Codex process generation is unavailable")
	ErrNilCheckpointSink        = errors.New("runtime checkpoint sink is required")
	ErrInvalidChild             = errors.New("invalid Codex App Server child")
	ErrCheckpointPersistence    = errors.New("runtime checkpoint persistence failed")
	ErrProcessCleanup           = errors.New("Codex App Server cleanup could not be proven")
	ErrInvalidSession           = errors.New("invalid Codex App Server session")
	ErrOwnerRepairRequired      = errors.New("Codex App Server owner repair is required")
	ErrPreviousProcessRunning   = errors.New("a prior Codex App Server process may still be running")
	ErrProcessIdentityMismatch  = errors.New("persisted Codex process identity does not match")
	ErrProcessIdentityUnknown   = errors.New("persisted Codex process identity cannot be proven")
	ErrProcessIdentityChanged   = errors.New("Codex process identity changed while inspected")
	ErrInvalidProcessCheckpoint = errors.New("invalid running Codex process checkpoint")
)

type VersionPolicy struct {
	AllowedCLI   map[string]struct{}
	SchemaSHA256 string
}

func DefaultVersionPolicy() VersionPolicy {
	return VersionPolicy{
		AllowedCLI:   map[string]struct{}{SupportedCLIOutput: {}},
		SchemaSHA256: SupportedSchemaSHA256,
	}
}

func (p VersionPolicy) ValidateCLI(output string) (string, error) {
	trimmed := strings.TrimSpace(output)
	if _, ok := p.AllowedCLI[trimmed]; !ok {
		return "", ErrUnsupportedCodexVersion
	}
	return trimmed, nil
}

func (p VersionPolicy) ValidateUserAgent(cliOutput, userAgent string) error {
	accepted, err := p.ValidateCLI(cliOutput)
	if err != nil {
		return err
	}
	version, ok := strings.CutPrefix(accepted, "codex-cli ")
	if !ok || version == "" || strings.IndexFunc(version, func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\n' }) >= 0 {
		return ErrInvalidVersionPolicy
	}
	fields := strings.Fields(userAgent)
	if len(fields) == 0 || fields[0] != "agent_romm/"+version {
		return ErrIncompatibleUserAgent
	}
	return nil
}

type OwnerRepairError struct {
	Reason error
}

func (e *OwnerRepairError) Error() string {
	if e == nil || e.Reason == nil {
		return ErrOwnerRepairRequired.Error()
	}
	return ErrOwnerRepairRequired.Error() + ": " + e.Reason.Error()
}

func (e *OwnerRepairError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Reason
}

func (e *OwnerRepairError) Is(target error) bool {
	return target == ErrOwnerRepairRequired || errors.Is(e.Reason, target)
}

type Session struct {
	Adapter *Adapter
	Child   *Child
	Done    <-chan struct{}
	Err     func() error

	supervisor *Supervisor
	rpc        *RPCClient
	cancel     context.CancelFunc
	rpcDone    chan struct{}
	stderrDone chan struct{}
	sink       CheckpointSink
	checkpoint room.RuntimeCheckpoint

	mu           sync.Mutex
	failure      error
	rpcResult    error
	terminal     chan struct{}
	terminalOnce sync.Once
	cleanupOnce  sync.Once
	cleanupDone  chan struct{}
	cleanupErr   error
	rpcCloseOnce sync.Once
	persistMu    sync.Mutex
	persisted    bool
}

type CheckpointSink func(context.Context, room.RuntimeCheckpoint) error

type Supervisor struct {
	executable     string
	policy         VersionPolicy
	processes      ProcessManager
	random         io.Reader
	stopGrace      time.Duration
	cleanupTimeout time.Duration
}

func NewSupervisor(executable string, policy VersionPolicy, processes ProcessManager) (*Supervisor, error) {
	if strings.TrimSpace(executable) == "" || strings.TrimSpace(executable) != executable {
		return nil, ErrInvalidProcessSpec
	}
	if processes == nil {
		return nil, ErrNilProcessManager
	}
	if len(policy.AllowedCLI) == 0 || policy.SchemaSHA256 == "" {
		return nil, ErrInvalidVersionPolicy
	}
	if policy.SchemaSHA256 != SupportedSchemaSHA256 {
		return nil, ErrUnsupportedSchema
	}
	for output := range policy.AllowedCLI {
		version, ok := strings.CutPrefix(output, "codex-cli ")
		if !ok || version == "" || strings.TrimSpace(output) != output || strings.ContainsAny(version, " \t\r\n") {
			return nil, ErrInvalidVersionPolicy
		}
	}
	return &Supervisor{
		executable: executable, policy: cloneVersionPolicy(policy), processes: processes,
		random: rand.Reader, stopGrace: defaultStopGrace, cleanupTimeout: defaultCleanupTimeout,
	}, nil
}

func NewDefaultSupervisor(executable string) (*Supervisor, error) {
	return NewSupervisor(executable, DefaultVersionPolicy(), NewProcessManager())
}

func cloneVersionPolicy(policy VersionPolicy) VersionPolicy {
	copyPolicy := VersionPolicy{AllowedCLI: make(map[string]struct{}, len(policy.AllowedCLI)), SchemaSHA256: policy.SchemaSHA256}
	for version := range policy.AllowedCLI {
		copyPolicy.AllowedCLI[version] = struct{}{}
	}
	return copyPolicy
}

func (s *Supervisor) Probe(ctx context.Context, projectRoot string) (room.RuntimeCheckpoint, error) {
	session, err := s.Start(ctx, projectRoot, discardSupervisorCheckpoint)
	if err != nil {
		return room.RuntimeCheckpoint{}, err
	}
	checkpoint := stoppedCheckpoint(session.checkpoint)
	if err := s.Stop(ctx, session); err != nil {
		return room.RuntimeCheckpoint{}, err
	}
	return checkpoint, nil
}

func discardSupervisorCheckpoint(context.Context, room.RuntimeCheckpoint) error { return nil }

func (s *Supervisor) ReapPrevious(ctx context.Context, checkpoint *room.RuntimeCheckpoint) error {
	if checkpoint == nil {
		return nil
	}
	if checkpoint.State == "" {
		if *checkpoint == (room.RuntimeCheckpoint{}) {
			return nil
		}
		return ownerRepair(ErrInvalidProcessCheckpoint)
	}
	if checkpoint.State == room.RuntimeProcessStopped {
		if checkpoint.PID != 0 || checkpoint.PGID != 0 {
			return ownerRepair(ErrInvalidProcessCheckpoint)
		}
		return nil
	}
	if checkpoint.State != room.RuntimeProcessRunning || checkpoint.PID <= 1 || checkpoint.PGID <= 1 ||
		strings.TrimSpace(checkpoint.Generation) == "" || strings.TrimSpace(checkpoint.ProcessStart) == "" {
		return ownerRepair(ErrInvalidProcessCheckpoint)
	}
	match, err := s.processes.Match(ctx, ProcessIdentity{
		Generation: checkpoint.Generation, PID: checkpoint.PID, PGID: checkpoint.PGID, StartIdentity: checkpoint.ProcessStart,
	})
	if err != nil {
		return ownerRepair(ErrProcessIdentityUnknown)
	}
	switch match {
	case ProcessAbsent:
		return nil
	case ProcessMatches:
		return ownerRepair(ErrPreviousProcessRunning)
	case ProcessMismatch:
		return ownerRepair(ErrProcessIdentityMismatch)
	default:
		return ownerRepair(ErrProcessIdentityUnknown)
	}
}

func ownerRepair(reason error) error { return &OwnerRepairError{Reason: reason} }

func (s *Supervisor) Start(ctx context.Context, projectRoot string, sink CheckpointSink) (*Session, error) {
	if sink == nil {
		return nil, ErrNilCheckpointSink
	}
	if !filepath.IsAbs(projectRoot) {
		return nil, ErrProjectRootNotAbsolute
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cliOutput, err := s.processes.Version(ctx, s.executable)
	if err != nil {
		return nil, safeError(ErrProcessVersion, err)
	}
	cliOutput, err = s.policy.ValidateCLI(cliOutput)
	if err != nil {
		return nil, err
	}
	generation, err := newProcessGeneration(s.random)
	if err != nil {
		return nil, ErrGenerationUnavailable
	}
	child, err := s.processes.Start(ctx, ProcessSpec{
		Executable: s.executable,
		Args:       []string{"app-server", "--listen", "stdio://"},
		Env:        append([]string(nil), os.Environ()...),
		Dir:        filepath.Clean(projectRoot),
		Generation: generation,
	})
	if err != nil {
		return nil, safeError(ErrProcessStart, err)
	}
	if err := validateChild(child, generation); err != nil {
		cleanupErr := s.stopCurrent(child)
		if cleanupErr != nil {
			return nil, errors.Join(err, safeError(ErrProcessCleanup, cleanupErr))
		}
		return nil, err
	}
	stderrDone := make(chan struct{})
	stderrRing := newBoundedRing(stderrRingBytes)
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrRing, child.Stderr)
	}()
	running := room.RuntimeCheckpoint{
		State: room.RuntimeProcessRunning, Generation: generation,
		PID: child.Identity.PID, PGID: child.Identity.PGID, ProcessStart: child.Identity.StartIdentity,
		CodexVersion: cliOutput, SchemaSHA256: s.policy.SchemaSHA256,
	}
	if err := sink(ctx, running); err != nil {
		cleanupErr := s.stopCurrent(child)
		closeChildPipes(child)
		<-stderrDone
		if cleanupErr != nil {
			return nil, errors.Join(safeError(ErrCheckpointPersistence, err), safeError(ErrProcessCleanup, cleanupErr))
		}
		return nil, safeError(ErrCheckpointPersistence, err)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	rpc := NewRPCClient(child.Stdout, child.Stdin)
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: projectRoot})
	if err != nil {
		cancel()
		cleanupErr := s.stopCurrent(child)
		closeChildPipes(child)
		<-stderrDone
		var checkpointErr error
		if cleanupErr == nil {
			if persistErr := s.persistStopped(sink, running); persistErr != nil {
				checkpointErr = safeError(ErrCheckpointPersistence, persistErr)
			}
		}
		return nil, errors.Join(err, normalizeOptionalCleanup(cleanupErr), checkpointErr)
	}
	session := newSession(s, adapter, rpc, child, cancel, stderrDone, sink, running)
	go session.runRPC(sessionCtx)
	go session.observeTerminal()

	initialize, err := adapter.Initialize(sessionCtx, cliOutput)
	if err == nil {
		err = s.policy.ValidateUserAgent(cliOutput, initialize.UserAgent)
	}
	if err != nil {
		stopErr := s.Stop(ctx, session)
		return nil, errors.Join(err, stopErr)
	}
	return session, nil
}

func newSession(
	supervisor *Supervisor,
	adapter *Adapter,
	rpc *RPCClient,
	child *Child,
	cancel context.CancelFunc,
	stderrDone chan struct{},
	sink CheckpointSink,
	checkpoint room.RuntimeCheckpoint,
) *Session {
	terminal := make(chan struct{})
	session := &Session{
		Adapter: adapter, Child: child, Done: terminal,
		supervisor: supervisor, rpc: rpc, cancel: cancel, rpcDone: make(chan struct{}), stderrDone: stderrDone,
		sink: sink, checkpoint: checkpoint, terminal: terminal, cleanupDone: make(chan struct{}),
	}
	session.Err = session.sessionErr
	return session
}

func (session *Session) runRPC(ctx context.Context) {
	err := session.rpc.Run(ctx)
	session.mu.Lock()
	session.rpcResult = err
	session.mu.Unlock()
	close(session.rpcDone)
}

func (session *Session) observeTerminal() {
	select {
	case <-session.rpcDone:
		session.mu.Lock()
		err := session.rpcResult
		session.mu.Unlock()
		session.publishTerminal("rpc", err)
	case <-session.Child.Done:
		err := session.Child.WaitErr()
		if errors.Is(err, ErrProcessObservation) {
			session.interruptRPC()
		}
		session.publishTerminal("process", err)
	}
}

func (session *Session) publishTerminal(source string, err error) {
	session.terminalOnce.Do(func() {
		session.mu.Lock()
		session.failure = normalizeSessionFailure(source, err)
		session.mu.Unlock()
		close(session.terminal)
	})
}

func (session *Session) sessionErr() error {
	select {
	case <-session.terminal:
	default:
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.failure
}

func (s *Supervisor) Stop(_ context.Context, session *Session) error {
	if session == nil || session.supervisor != s {
		return ErrInvalidSession
	}
	session.cleanupOnce.Do(func() {
		session.cleanupErr = s.stopSession(session)
		close(session.cleanupDone)
	})
	<-session.cleanupDone
	if session.cleanupErr != nil {
		return session.cleanupErr
	}
	session.persistMu.Lock()
	defer session.persistMu.Unlock()
	if session.persisted {
		return nil
	}
	if err := s.persistStopped(session.sink, session.checkpoint); err != nil {
		return safeError(ErrCheckpointPersistence, err)
	}
	session.persisted = true
	return nil
}

func (s *Supervisor) stopSession(session *Session) error {
	session.mu.Lock()
	adapter := session.Adapter
	session.Adapter = nil
	session.mu.Unlock()
	if adapter != nil {
		adapter.Close()
	}
	session.interruptRPC()
	processErr := s.stopCurrent(session.Child)
	<-session.rpcDone
	<-session.stderrDone
	if processErr != nil {
		return safeError(ErrProcessCleanup, processErr)
	}
	<-session.Child.Done
	return nil
}

func (session *Session) interruptRPC() {
	session.rpcCloseOnce.Do(func() {
		if session.cancel != nil {
			session.cancel()
		}
		closeChildPipes(session.Child)
	})
}

func (s *Supervisor) stopCurrent(child *Child) error {
	ctx, cancel := s.newCleanupContext()
	defer cancel()
	return s.processes.StopCurrent(ctx, child, s.stopGrace)
}

func (s *Supervisor) persistStopped(sink CheckpointSink, running room.RuntimeCheckpoint) error {
	ctx, cancel := s.newCleanupContext()
	defer cancel()
	return sink(ctx, stoppedCheckpoint(running))
}

func (s *Supervisor) newCleanupContext() (context.Context, context.CancelFunc) {
	timeout := s.cleanupTimeout
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

func stoppedCheckpoint(running room.RuntimeCheckpoint) room.RuntimeCheckpoint {
	running.State = room.RuntimeProcessStopped
	running.PID = 0
	running.PGID = 0
	return running
}

func closeChildPipes(child *Child) {
	if child == nil {
		return
	}
	if child.Stdin != nil {
		_ = child.Stdin.Close()
	}
	if child.Stdout != nil {
		_ = child.Stdout.Close()
	}
	if child.Stderr != nil {
		_ = child.Stderr.Close()
	}
}

func validateChild(child *Child, generation string) error {
	if child == nil || child.Stdin == nil || child.Stdout == nil || child.Stderr == nil || child.Done == nil || child.WaitErr == nil ||
		child.Identity.Generation != generation || child.Identity.PID <= 1 || child.Identity.PGID <= 1 || strings.TrimSpace(child.Identity.StartIdentity) == "" {
		return ErrInvalidChild
	}
	return nil
}

func newProcessGeneration(source io.Reader) (string, error) {
	data := make([]byte, processGenerationBytes)
	if _, err := io.ReadFull(source, data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func normalizeOptionalCleanup(err error) error {
	if err == nil {
		return nil
	}
	return safeError(ErrProcessCleanup, err)
}

type safeDigestError struct {
	cause          error
	classification error
	digest         string
}

func safeError(cause, detail error) error {
	if detail == nil {
		return cause
	}
	digest := sha256.Sum256([]byte(detail.Error()))
	var classification error
	switch {
	case errors.Is(detail, ErrUnsupportedPlatform):
		classification = ErrUnsupportedPlatform
	case errors.Is(detail, context.DeadlineExceeded):
		classification = context.DeadlineExceeded
	case errors.Is(detail, context.Canceled):
		classification = context.Canceled
	}
	return &safeDigestError{cause: cause, classification: classification, digest: hex.EncodeToString(digest[:])}
}

func (e *safeDigestError) Error() string {
	return fmt.Sprintf("%s (detail digest %s)", e.cause, e.digest)
}

func (e *safeDigestError) Unwrap() error { return e.cause }

func (e *safeDigestError) Is(target error) bool {
	return errors.Is(e.cause, target) || e.classification != nil && errors.Is(e.classification, target)
}

type SessionFailureError struct {
	Code   string
	Digest string
}

func (e *SessionFailureError) Error() string {
	return fmt.Sprintf("Codex App Server session failed (%s, digest %s)", e.Code, e.Digest)
}

func normalizeSessionFailure(source string, err error) error {
	code := source + "-exit"
	if source == "rpc" {
		switch {
		case errors.Is(err, context.Canceled):
			code = "rpc-canceled"
		case errors.Is(err, io.EOF):
			code = "rpc-eof"
		default:
			code = "rpc-failure"
		}
	}
	detail := code
	if err != nil {
		detail = err.Error()
	}
	digest := sha256.Sum256([]byte(detail))
	return &SessionFailureError{Code: code, Digest: hex.EncodeToString(digest[:])}
}

type boundedRing struct {
	mu       sync.Mutex
	data     []byte
	capacity int
}

func newBoundedRing(capacity int) *boundedRing {
	return &boundedRing{data: make([]byte, 0, capacity), capacity: capacity}
}

func (r *boundedRing) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	original := len(data)
	if len(data) >= r.capacity {
		r.data = append(r.data[:0], data[len(data)-r.capacity:]...)
		return original, nil
	}
	overflow := len(r.data) + len(data) - r.capacity
	if overflow > 0 {
		copy(r.data, r.data[overflow:])
		r.data = r.data[:len(r.data)-overflow]
	}
	r.data = append(r.data, data...)
	return original, nil
}
