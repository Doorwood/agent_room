package room

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrStaleTurn          = errors.New("expected turn is no longer active")
	ErrAgentRuntimeClosed = errors.New("agent runtime event stream closed")
	ErrDeliveryNotSent    = errors.New("agent mutation was not sent")
	ErrDeliveryUnknown    = errors.New("agent mutation delivery is uncertain")
)

type coordinatorState struct {
	status          RoomStatus
	threadID        ThreadID
	active          *TurnBinding
	pendingControls []TurnBinding
	queue           []QueuedMessage
	latestSeq       Seq
}

type Coordinator struct {
	roomID                  RoomID
	projectRoot             string
	repository              Repository
	agent                   Agent
	sink                    EventSink
	clock                   Clock
	commands                chan any
	agentEvents             <-chan AgentEvent
	state                   coordinatorState
	threadCreationUncertain bool
	runtimeReadySeen        bool
	threadReady             bool
	projection              *Projection
	fatalErr                error
	activeFault             string
}

func NewCoordinator(roomID RoomID, projectRoot string, repository Repository, agent Agent, sink EventSink, clock Clock) (*Coordinator, error) {
	if strings.TrimSpace(string(roomID)) == "" || strings.TrimSpace(projectRoot) == "" || repository == nil || agent == nil || sink == nil || clock == nil {
		return nil, errors.New("invalid coordinator dependency")
	}
	return &Coordinator{roomID: roomID, projectRoot: projectRoot, repository: repository, agent: agent, sink: sink, clock: clock, commands: make(chan any), projection: NewProjection(), agentEvents: agent.Events()}, nil
}

func (c *Coordinator) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case command := <-c.commands:
			c.handleCommand(command)
		case event, ok := <-c.agentEvents:
			if !ok {
				return ErrAgentRuntimeClosed
			}
			c.handleAgentEvent(ctx, event)
			if c.fatalErr != nil {
				return c.fatalErr
			}
		}
	}
}

func (c *Coordinator) Recover(ctx context.Context) error {
	res := make(chan error, 1)
	if err := c.send(ctx, recoverCommand{ctx: ctx, res: res}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-res:
		return err
	}
}

func (c *Coordinator) Submit(ctx context.Context, actor Actor, input SubmitInput) (Acceptance, error) {
	if err := input.Validate(); err != nil {
		return Acceptance{}, err
	}
	return c.submit(ctx, actor, input, false)
}

func (c *Coordinator) Note(ctx context.Context, actor Actor, input SubmitInput) (Acceptance, error) {
	if err := input.Validate(); err != nil {
		return Acceptance{}, err
	}
	return c.submit(ctx, actor, input, true)
}

func (c *Coordinator) submit(ctx context.Context, actor Actor, input SubmitInput, note bool) (Acceptance, error) {
	res := make(chan result[Acceptance], 1)
	if err := c.send(ctx, submitCommand{ctx: ctx, actor: actor, input: input, note: note, res: res}); err != nil {
		return Acceptance{}, err
	}
	select {
	case <-ctx.Done():
		return Acceptance{}, ctx.Err()
	case got := <-res:
		return got.value, got.err
	}
}

func (c *Coordinator) Steer(ctx context.Context, actor Actor, input SteerInput) (Acceptance, error) {
	if err := input.Validate(); err != nil {
		return Acceptance{}, err
	}
	return c.control(ctx, actor, ControlInput{ClientMessageID: input.ClientMessageID, Kind: ControlSteer, ExpectedTurnID: input.ExpectedTurnID, Text: input.Text})
}
func (c *Coordinator) Cancel(ctx context.Context, actor Actor, input CancelInput) (Acceptance, error) {
	if err := input.Validate(); err != nil {
		return Acceptance{}, err
	}
	return c.control(ctx, actor, ControlInput{ClientMessageID: input.ClientMessageID, Kind: ControlCancel, ExpectedTurnID: input.ExpectedTurnID})
}
func (c *Coordinator) control(ctx context.Context, actor Actor, input ControlInput) (Acceptance, error) {
	res := make(chan result[Acceptance], 1)
	if err := c.send(ctx, controlCommand{ctx: ctx, actor: actor, input: input, res: res}); err != nil {
		return Acceptance{}, err
	}
	select {
	case <-ctx.Done():
		return Acceptance{}, ctx.Err()
	case got := <-res:
		return got.value, got.err
	}
}
func (c *Coordinator) Snapshot(ctx context.Context) (Snapshot, error) {
	res := make(chan result[Snapshot], 1)
	if err := c.send(ctx, snapshotCommand{ctx: ctx, res: res}); err != nil {
		return Snapshot{}, err
	}
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case got := <-res:
		return got.value, got.err
	}
}
func (c *Coordinator) send(ctx context.Context, command any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.commands <- command:
		return nil
	}
}

func (c *Coordinator) handleCommand(command any) {
	switch cmd := command.(type) {
	case resolveCommand:
		cmd.res <- c.resolve(cmd.ctx, cmd.actor, cmd.input)
	case recoverCommand:
		cmd.res <- c.recover(cmd.ctx)
	case submitCommand:
		got, err := c.handleSubmit(cmd.ctx, cmd.actor, cmd.input, cmd.note)
		cmd.res <- result[Acceptance]{got, err}
	case controlCommand:
		got, err := c.handleControl(cmd.ctx, cmd.actor, cmd.input)
		cmd.res <- result[Acceptance]{got, err}
	case snapshotCommand:
		cmd.res <- result[Snapshot]{value: c.snapshot()}
	}
}

func (c *Coordinator) handleSubmit(ctx context.Context, actor Actor, input SubmitInput, note bool) (Acceptance, error) {
	var got Acceptance
	var err error
	if note {
		got, err = c.repository.AppendNote(ctx, c.roomID, actor, input)
	} else {
		got, err = c.repository.AcceptMessage(ctx, c.roomID, actor, input)
	}
	if err != nil {
		return Acceptance{}, err
	}
	c.publish(got.Event)
	if got.Duplicate || note {
		return got, nil
	}
	c.state.queue = append(c.state.queue, QueuedMessage{Input: input, Actor: actor, AcceptedSeq: got.Seq})
	if err := c.dispatchNext(ctx); err != nil {
		return got, err
	}
	return got, nil
}

func (c *Coordinator) dispatchNext(ctx context.Context) error {
	if c.state.status != RoomReady || c.state.active != nil || len(c.state.pendingControls) != 0 || len(c.state.queue) == 0 {
		return nil
	}
	next := c.state.queue[0]
	if err := c.repository.BeginDispatch(ctx, c.roomID, next.Input.ClientMessageID); err != nil {
		return err
	}
	c.state.active = &TurnBinding{MessageID: next.Input.ClientMessageID, State: RequestDispatching}
	turnID, err := c.agent.StartTurn(ctx, c.state.threadID, next.Input.ClientMessageID, "[participant: "+next.Actor.Name+"]\n"+next.Input.Text)
	if err != nil {
		code, digest, certainty := mutationFailure(err)
		if certainty == DeliveryNotSent {
			event, storeErr := c.repository.FailDispatch(ctx, c.roomID, next.Input.ClientMessageID, FailureOutcome{State: RequestFailed, ErrorCode: code, ErrorDigest: digest})
			if storeErr != nil {
				return storeErr
			}
			c.publish(event)
			c.state.queue = c.state.queue[1:]
			c.state.active = nil
			_ = c.dispatchNext(ctx)
			return ErrDeliveryNotSent
		}
		c.state.status = RoomRecovering
		event, storeErr := c.repository.MarkNeedsReview(ctx, c.roomID, next.Input.ClientMessageID, ReviewReason{Code: code, DetailDigest: digest})
		if storeErr != nil {
			return storeErr
		}
		c.publish(event)
		c.state.active.State = RequestNeedsReview
		c.state.queue = c.state.queue[1:]
		if statusEvent, statusErr := c.repository.SetRoomStatus(ctx, c.roomID, RoomRecovering); statusErr != nil {
			return statusErr
		} else if statusEvent != nil {
			c.publish(*statusEvent)
		}
		return ErrDeliveryUnknown
	}
	// Freeze on a lost persistence result: turn/start has already run.
	event, err := c.repository.BindRunningTurn(ctx, c.roomID, next.Input.ClientMessageID, turnID)
	if err != nil {
		c.state.status = RoomRecovering
		return err
	}
	c.publish(event)
	c.state.queue = c.state.queue[1:]
	c.state.active = &TurnBinding{MessageID: next.Input.ClientMessageID, TurnID: turnID, State: RequestRunning}
	return nil
}

func (c *Coordinator) handleControl(ctx context.Context, actor Actor, input ControlInput) (Acceptance, error) {
	got, err := c.repository.AcceptControl(ctx, c.roomID, actor, input)
	if err != nil {
		return Acceptance{}, err
	}
	c.publish(got.Event)
	if got.Duplicate {
		if got.ErrorCode == "stale-turn" {
			return got, ErrStaleTurn
		}
		if got.State == RequestNeedsReview {
			return got, ErrDeliveryUnknown
		}
		if got.State == RequestFailed {
			return got, ErrDeliveryNotSent
		}
		return got, nil
	}
	c.state.pendingControls = append(c.state.pendingControls, TurnBinding{MessageID: got.ClientMessageID, TurnID: input.ExpectedTurnID, State: RequestDispatching})
	if c.state.status != RoomReady || c.state.active == nil || c.state.active.State != RequestRunning || c.state.active.TurnID != input.ExpectedTurnID {
		return c.finishControl(ctx, got, ControlOutcome{State: RequestFailed, ErrorCode: "stale-turn"}, ErrStaleTurn)
	}
	if input.Kind == ControlSteer {
		err = c.agent.SteerTurn(ctx, c.state.threadID, input.ExpectedTurnID, input.Text)
	} else {
		err = c.agent.InterruptTurn(ctx, c.state.threadID, input.ExpectedTurnID)
	}
	if err == nil {
		return c.finishControl(ctx, got, ControlOutcome{State: RequestCompleted}, nil)
	}
	code, digest, certainty := mutationFailure(err)
	if certainty == DeliveryNotSent {
		return c.finishControl(ctx, got, ControlOutcome{State: RequestFailed, ErrorCode: code, ErrorDigest: digest}, ErrDeliveryNotSent)
	}
	c.state.status = RoomRecovering
	finished, finishErr := c.finishControl(ctx, got, ControlOutcome{State: RequestNeedsReview, ErrorCode: code, ErrorDigest: digest}, ErrDeliveryUnknown)
	if finishErr != nil && !errors.Is(finishErr, ErrDeliveryUnknown) {
		return finished, finishErr
	}
	if statusEvent, statusErr := c.repository.SetRoomStatus(ctx, c.roomID, RoomRecovering); statusErr != nil {
		return finished, statusErr
	} else if statusEvent != nil {
		c.publish(*statusEvent)
	}
	return finished, ErrDeliveryUnknown
}
func (c *Coordinator) finishControl(ctx context.Context, got Acceptance, outcome ControlOutcome, resultErr error) (Acceptance, error) {
	event, err := c.repository.FinishControl(ctx, c.roomID, got.ClientMessageID, outcome)
	if err != nil {
		c.state.status = RoomRecovering
		return got, err
	}
	for i := range c.state.pendingControls {
		if c.state.pendingControls[i].MessageID != got.ClientMessageID {
			continue
		}
		if outcome.State == RequestNeedsReview {
			c.state.pendingControls[i].State = RequestNeedsReview
		} else {
			c.state.pendingControls = append(c.state.pendingControls[:i], c.state.pendingControls[i+1:]...)
		}
		break
	}
	c.publish(event)
	got.State = outcome.State
	got.ErrorCode = outcome.ErrorCode
	return got, resultErr
}

func (c *Coordinator) handleAgentEvent(ctx context.Context, event AgentEvent) {
	switch event.Kind {
	case "item-delta", "item-completed":
		turnID := event.TurnID
		if event.Completed != nil {
			turnID = event.Completed.TurnID
		}
		if c.state.active == nil || (c.state.active.State != RequestRunning && !(c.activeFault != "" && c.state.active.State == RequestNeedsReview)) || c.state.active.TurnID != turnID {
			return
		}
		if event.Completed != nil {
			if event.Completed.ThreadID != c.state.threadID {
				return
			}
			durable, err := c.repository.RecordCompletedItem(ctx, c.roomID, *event.Completed)
			if err != nil {
				c.failPersistence(err)
				return
			}
			c.publish(durable)
		} else if event.ThreadID != c.state.threadID {
			return
		}
		update := c.projection.Apply(event)
		if update.Transient != nil {
			c.sink.PublishTransient(*update.Transient)
		}
	case "turn-completed", "turn-failed", "turn-interrupted":
		if c.state.active == nil || (c.state.active.State != RequestRunning && !(c.activeFault != "" && c.state.active.State == RequestNeedsReview)) || c.state.active.TurnID != event.TurnID || (event.ThreadID != "" && event.ThreadID != c.state.threadID) {
			return
		}
		state := RequestCompleted
		finish := FinishTurnInput{TurnID: event.TurnID, State: state}
		if event.Kind == "turn-failed" {
			state = RequestFailed
		}
		if event.Kind == "turn-interrupted" {
			state = RequestInterrupted
		}
		finish.State = state
		if c.activeFault != "" {
			finish.State = RequestFailed
			finish.ErrorCode = c.activeFault
		}
		if state != RequestCompleted {
			if finish.ErrorCode == "" {
				finish.ErrorCode = event.Kind
			}
			if event.Error != nil {
				finish.ErrorDigest = digestError(event.Error)
			}
		}
		if events, err := c.repository.FinishTurn(ctx, c.roomID, finish); err == nil {
			for _, e := range events {
				c.publish(e)
			}
			for _, removal := range c.projection.removeTurn(c.state.threadID, event.TurnID) {
				c.sink.PublishTransient(removal)
			}
			c.state.active = nil
			if c.activeFault != "" {
				c.activeFault = ""
				statusEvent, statusErr := c.repository.SetRoomStatus(ctx, c.roomID, RoomReady)
				if statusErr != nil {
					c.failPersistence(statusErr)
					return
				}
				if statusEvent != nil {
					c.publish(*statusEvent)
				}
				c.state.status = RoomReady
			}
			if err := c.dispatchNext(ctx); err != nil && !errors.Is(err, ErrDeliveryNotSent) && !errors.Is(err, ErrDeliveryUnknown) {
				c.failPersistence(err)
			}
		} else {
			c.failPersistence(err)
		}
	case "unsupported-server-request", "protocol-error":
		if c.state.active == nil {
			c.failPersistence(fmt.Errorf("agent %s without active turn", event.Kind))
			return
		}
		if c.activeFault != "" {
			return
		}
		if event.ThreadID != "" && event.ThreadID != c.state.threadID || event.TurnID != "" && event.TurnID != c.state.active.TurnID {
			return
		}
		c.state.status = RoomRecovering
		c.activeFault = event.Kind
		diagnostic := event.Error
		if diagnostic == nil {
			diagnostic = errors.New(event.Kind)
		}
		e, err := c.repository.MarkNeedsReview(ctx, c.roomID, c.state.active.MessageID, ReviewReason{Code: event.Kind, DetailDigest: digestError(diagnostic)})
		if err != nil {
			c.failPersistence(err)
			return
		}
		c.publish(e)
		c.state.active.State = RequestNeedsReview
		statusEvent, err := c.repository.SetRoomStatus(ctx, c.roomID, RoomRecovering)
		if err != nil {
			c.failPersistence(err)
			return
		}
		if statusEvent != nil {
			c.publish(*statusEvent)
		}
		// A successful interrupt reply is not proof of termination. Keep the
		// queue frozen until a matching terminal event or recovered history.
		_ = c.agent.InterruptTurn(ctx, c.state.threadID, c.state.active.TurnID)
	case "runtime-unavailable":
		c.activeFault = ""
		c.threadReady = false
		c.runtimeReadySeen = false
		c.resetProjection()
		if c.state.status == RoomRecovering {
			return
		}
		c.state.status = RoomRecovering
		if e, err := c.repository.SetRoomStatus(ctx, c.roomID, RoomRecovering); err == nil && e != nil {
			c.publish(*e)
		}
		if c.state.active != nil && c.state.active.State != RequestNeedsReview {
			diagnostic := event.Error
			if diagnostic == nil {
				diagnostic = ErrAgentRuntimeClosed
			}
			if e, err := c.repository.MarkNeedsReview(ctx, c.roomID, c.state.active.MessageID, ReviewReason{Code: "runtime-unavailable", DetailDigest: digestError(diagnostic)}); err == nil {
				c.publish(e)
				c.state.active.State = RequestNeedsReview
			}
		}
	case "runtime-ready":
		if c.state.status != RoomReady && !c.runtimeReadySeen {
			c.runtimeReadySeen = true
			_ = c.recover(ctx)
		}
	}
}

func (c *Coordinator) failPersistence(err error) {
	c.state.status = RoomRecovering
	c.fatalErr = fmt.Errorf("coordinator durable transition failed: %w", err)
}

func (c *Coordinator) snapshot() Snapshot {
	revision, liveItems := c.projection.Snapshot()
	s := Snapshot{Status: c.state.status, ThreadID: c.state.threadID, Queue: append([]QueuedMessage(nil), c.state.queue...), ProjectionRevision: revision, LiveItems: liveItems, LatestSeq: c.state.latestSeq}
	s.Active = copyBinding(c.state.active)
	s.ProjectionTruncated = c.projection.truncated
	return s
}
func copyBinding(in *TurnBinding) *TurnBinding {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
func (c *Coordinator) publish(event DurableEvent) {
	if event.Seq > c.state.latestSeq {
		c.state.latestSeq = event.Seq
	}
	c.sink.PublishDurable(event)
}
func mutationFailure(err error) (string, string, DeliveryCertainty) {
	var mutation *MutationError
	if errors.As(err, &mutation) {
		if mutation.Certainty == DeliveryNotSent {
			return "delivery-not-sent", digestError(mutation.Err), DeliveryNotSent
		}
	}
	return "delivery-unknown", digestError(err), DeliveryUnknown
}
func normalizedMutationError(err error) error {
	_, _, certainty := mutationFailure(err)
	if certainty == DeliveryNotSent {
		return ErrDeliveryNotSent
	}
	return ErrDeliveryUnknown
}
func digestError(err error) string {
	if err == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(err.Error()))
	return fmt.Sprintf("%x", sum[:])
}
