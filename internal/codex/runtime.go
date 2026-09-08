package codex

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"agent_romm/internal/room"
)

const (
	defaultRestartInitial = 250 * time.Millisecond
	defaultRestartMaximum = 10 * time.Second
	defaultExitDrain      = 250 * time.Millisecond
	runtimeEventCapacity  = 1
)

var (
	ErrNilSupervisor         = errors.New("Codex supervisor is required")
	ErrNilSleeper            = errors.New("runtime sleeper is required")
	ErrInvalidRuntimeConfig  = errors.New("invalid Codex runtime configuration")
	ErrRuntimeAlreadyStarted = errors.New("Codex runtime is already started")
	ErrRuntimeClosed         = errors.New("Codex runtime is closed")
	ErrRuntimeUnavailable    = errors.New("Codex runtime is unavailable")
)

type RuntimeConfig struct {
	ProjectRoot    string
	RestartInitial time.Duration
	RestartMaximum time.Duration
}

type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type timerSleeper struct{}

func (timerSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Runtime struct {
	supervisor     *Supervisor
	config         RuntimeConfig
	sink           CheckpointSink
	sleeper        Sleeper
	events         chan room.AgentEvent
	exitDrainAfter func(time.Duration) <-chan time.Time

	mu           sync.RWMutex
	started      bool
	starting     bool
	closed       bool
	ctx          context.Context
	cancel       context.CancelFunc
	session      *Session
	adapter      *Adapter
	startupDone  chan struct{}
	ownerDone    chan struct{}
	ownerStarted bool
	runtimeErr   error

	closeEventsOnce sync.Once
}

var _ room.Agent = (*Runtime)(nil)

func NewRuntime(supervisor *Supervisor, config RuntimeConfig, sink CheckpointSink, sleeper Sleeper) (*Runtime, error) {
	if supervisor == nil {
		return nil, ErrNilSupervisor
	}
	if sink == nil {
		return nil, ErrNilCheckpointSink
	}
	if sleeper == nil {
		return nil, ErrNilSleeper
	}
	if !filepath.IsAbs(config.ProjectRoot) {
		return nil, ErrInvalidRuntimeConfig
	}
	config.ProjectRoot = filepath.Clean(config.ProjectRoot)
	if config.RestartInitial == 0 {
		config.RestartInitial = defaultRestartInitial
	}
	if config.RestartMaximum == 0 {
		config.RestartMaximum = defaultRestartMaximum
	}
	if config.RestartInitial <= 0 || config.RestartMaximum <= 0 || config.RestartInitial > config.RestartMaximum {
		return nil, ErrInvalidRuntimeConfig
	}
	return &Runtime{
		supervisor: supervisor, config: config, sink: sink, sleeper: sleeper,
		events: make(chan room.AgentEvent, runtimeEventCapacity), ownerDone: make(chan struct{}),
		exitDrainAfter: time.After,
	}, nil
}

func NewDefaultRuntime(supervisor *Supervisor, config RuntimeConfig, sink CheckpointSink) (*Runtime, error) {
	return NewRuntime(supervisor, config, sink, timerSleeper{})
}

func (r *Runtime) Events() <-chan room.AgentEvent { return r.events }

func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrRuntimeClosed
	}
	if r.started {
		r.mu.Unlock()
		return ErrRuntimeAlreadyStarted
	}
	r.started = true
	r.starting = true
	r.startupDone = make(chan struct{})
	r.ctx, r.cancel = context.WithCancel(ctx)
	runtimeCtx := r.ctx
	r.mu.Unlock()

	session, err := r.supervisor.Start(runtimeCtx, r.config.ProjectRoot, r.sink)
	if err != nil {
		r.mu.Lock()
		r.starting = false
		closed := r.closed
		close(r.startupDone)
		r.mu.Unlock()
		if closed {
			r.closeEvents()
		}
		return err
	}

	r.mu.Lock()
	closed := r.closed || runtimeCtx.Err() != nil
	if closed {
		r.mu.Unlock()
		stopErr := r.supervisor.Stop(runtimeCtx, session)
		r.recordRuntimeError(stopErr)
		r.mu.Lock()
		r.starting = false
		close(r.startupDone)
		r.mu.Unlock()
		r.closeEvents()
		if stopErr != nil {
			return stopErr
		}
		if err := runtimeCtx.Err(); err != nil {
			return err
		}
		return ErrRuntimeClosed
	}
	r.session = session
	r.adapter = session.Adapter
	r.starting = false
	r.ownerStarted = true
	close(r.startupDone)
	r.mu.Unlock()
	go r.run(session)
	return nil
}

func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		if r.cancel != nil {
			r.cancel()
		}
	}
	started := r.started
	startupDone := r.startupDone
	r.mu.Unlock()

	if !started {
		r.closeEvents()
		return r.currentRuntimeError()
	}
	if startupDone != nil {
		select {
		case <-startupDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.RLock()
	ownerStarted := r.ownerStarted
	r.mu.RUnlock()
	if !ownerStarted {
		r.closeEvents()
		return r.currentRuntimeError()
	}
	select {
	case <-r.ownerDone:
		return r.currentRuntimeError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) run(session *Session) {
	defer close(r.ownerDone)
	defer r.closeEvents()
	for {
		failed, stop := r.forwardSession(session)
		if stop {
			r.removeSession(session)
			r.recordRuntimeError(r.supervisor.Stop(r.ctx, session))
			return
		}
		if !failed {
			continue
		}
		r.removeSession(session)
		if !r.drainFailedSession(session) {
			r.recordRuntimeError(r.supervisor.Stop(r.ctx, session))
			return
		}
		if !r.publish(room.AgentEvent{Kind: "runtime-unavailable", Error: session.Err()}) {
			r.recordRuntimeError(r.supervisor.Stop(r.ctx, session))
			return
		}
		delay := r.config.RestartInitial
		stopErr := r.supervisor.Stop(r.ctx, session)
		if stopErr == nil {
			if err := r.sleeper.Sleep(r.ctx, delay); err != nil {
				return
			}
		} else {
			for errors.Is(stopErr, ErrCheckpointPersistence) {
				if err := r.sleeper.Sleep(r.ctx, delay); err != nil {
					r.recordRuntimeError(stopErr)
					return
				}
				stopErr = r.supervisor.Stop(r.ctx, session)
				if stopErr != nil {
					delay = nextBackoff(delay, r.config.RestartMaximum)
				}
			}
			if stopErr != nil {
				r.recordRuntimeError(stopErr)
				r.recoverWithoutStartingAt(delay)
				return
			}
		}

		for {
			replacement, err := r.supervisor.Start(r.ctx, r.config.ProjectRoot, r.sink)
			if err != nil {
				if errors.Is(err, ErrProcessCleanup) || errors.Is(err, ErrCheckpointPersistence) {
					r.recordRuntimeError(err)
					r.recoverWithoutStartingAt(nextBackoff(delay, r.config.RestartMaximum))
					return
				}
				delay = nextBackoff(delay, r.config.RestartMaximum)
				if err := r.sleeper.Sleep(r.ctx, delay); err != nil {
					return
				}
				continue
			}
			if !r.installReplacement(replacement) {
				r.recordRuntimeError(r.supervisor.Stop(r.ctx, replacement))
				return
			}
			if !r.publish(room.AgentEvent{Kind: "runtime-ready"}) {
				r.removeSession(replacement)
				r.recordRuntimeError(r.supervisor.Stop(r.ctx, replacement))
				return
			}
			session = replacement
			break
		}
	}
}

func (r *Runtime) forwardSession(session *Session) (failed bool, stop bool) {
	events := session.Adapter.Events()
	for {
		if events == nil {
			select {
			case <-r.ctx.Done():
				return false, true
			case <-session.Done:
				r.removeSession(session)
				return true, false
			}
		}
		select {
		case <-r.ctx.Done():
			return false, true
		case <-session.Done:
			r.removeSession(session)
			return true, false
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			failed, stop := r.publishSessionEvent(session, event)
			if failed || stop {
				return failed, stop
			}
		}
	}
}

func (r *Runtime) publishSessionEvent(session *Session, event room.AgentEvent) (failed bool, stop bool) {
	done := session.Done
	for {
		select {
		case <-done:
			r.removeSession(session)
			failed = true
			done = nil
		default:
		}
		select {
		case r.events <- event:
			return failed, false
		case <-r.ctx.Done():
			return false, true
		case <-done:
			r.removeSession(session)
			failed = true
			done = nil
		}
	}
}

func (r *Runtime) drainFailedSession(session *Session) bool {
	events := session.Adapter.Events()
	after := r.exitDrainAfter
	if after == nil {
		after = time.After
	}
	deadline := after(defaultExitDrain)
	for {
		select {
		case <-r.ctx.Done():
			return false
		case <-deadline:
			session.interruptRPC()
			deadline = nil
		case event, ok := <-events:
			if !ok {
				return true
			}
			if !r.publish(event) {
				return false
			}
		}
	}
}

func (r *Runtime) publish(event room.AgentEvent) bool {
	select {
	case r.events <- event:
		return true
	case <-r.ctx.Done():
		return false
	}
}

func (r *Runtime) removeSession(session *Session) {
	r.mu.Lock()
	if r.session == session {
		r.session = nil
		r.adapter = nil
	}
	r.mu.Unlock()
}

func (r *Runtime) installReplacement(session *Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil || r.session != nil {
		return false
	}
	r.session = session
	r.adapter = session.Adapter
	return true
}

func (r *Runtime) recoverWithoutStarting() {
	r.recoverWithoutStartingAt(r.config.RestartInitial)
}

func (r *Runtime) recoverWithoutStartingAt(delay time.Duration) {
	for {
		if err := r.sleeper.Sleep(r.ctx, delay); err != nil {
			return
		}
		delay = nextBackoff(delay, r.config.RestartMaximum)
	}
}

func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}

func (r *Runtime) closeEvents() {
	r.closeEventsOnce.Do(func() { close(r.events) })
}

func (r *Runtime) recordRuntimeError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.runtimeErr == nil {
		r.runtimeErr = err
	}
	r.mu.Unlock()
}

func (r *Runtime) currentRuntimeError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.runtimeErr
}

func (r *Runtime) snapshotAdapter() (*Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.adapter == nil {
		return nil, ErrRuntimeUnavailable
	}
	return r.adapter, nil
}

func (r *Runtime) StartThread(ctx context.Context) (room.ThreadSnapshot, error) {
	adapter, err := r.snapshotAdapter()
	if err != nil {
		return room.ThreadSnapshot{}, unavailableMutation("thread/start")
	}
	return adapter.StartThread(ctx)
}

func (r *Runtime) ReadThread(ctx context.Context, threadID room.ThreadID) (room.ThreadSnapshot, error) {
	adapter, err := r.snapshotAdapter()
	if err != nil {
		return room.ThreadSnapshot{}, err
	}
	return adapter.ReadThread(ctx, threadID)
}

func (r *Runtime) ResumeThread(ctx context.Context, threadID room.ThreadID) (room.ThreadSnapshot, error) {
	adapter, err := r.snapshotAdapter()
	if err != nil {
		return room.ThreadSnapshot{}, err
	}
	return adapter.ResumeThread(ctx, threadID)
}

func (r *Runtime) StartTurn(ctx context.Context, threadID room.ThreadID, clientMessageID room.ClientMessageID, text string) (room.TurnID, error) {
	adapter, err := r.snapshotAdapter()
	if err != nil {
		return "", unavailableMutation("turn/start")
	}
	return adapter.StartTurn(ctx, threadID, clientMessageID, text)
}

func (r *Runtime) SteerTurn(ctx context.Context, threadID room.ThreadID, turnID room.TurnID, text string) error {
	adapter, err := r.snapshotAdapter()
	if err != nil {
		return unavailableMutation("turn/steer")
	}
	return adapter.SteerTurn(ctx, threadID, turnID, text)
}

func (r *Runtime) InterruptTurn(ctx context.Context, threadID room.ThreadID, turnID room.TurnID) error {
	adapter, err := r.snapshotAdapter()
	if err != nil {
		return unavailableMutation("turn/interrupt")
	}
	return adapter.InterruptTurn(ctx, threadID, turnID)
}

func unavailableMutation(operation string) error {
	return &room.MutationError{Operation: operation, Certainty: room.DeliveryNotSent, Err: ErrRuntimeUnavailable}
}
