package room

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrStaleRecovery = errors.New("recovery target is no longer awaiting review")

func (c *Coordinator) Resolve(ctx context.Context, actor Actor, input RecoverInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	res := make(chan error, 1)
	if err := c.send(ctx, resolveCommand{ctx: ctx, actor: actor, input: input, res: res}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-res:
		return err
	}
}

func (c *Coordinator) resetProjection() {
	for _, event := range c.projection.reset() {
		c.sink.PublishTransient(event)
	}
}

func (c *Coordinator) loadRecovery(ctx context.Context) (RecoveryImage, error) {
	image, err := c.repository.LoadRecoveryImage(ctx, c.roomID)
	if err != nil {
		return image, err
	}
	latest, err := c.repository.LatestSeq(ctx, c.roomID)
	if err != nil {
		return image, err
	}
	if latest < c.state.latestSeq {
		latest = c.state.latestSeq
	}
	c.state = coordinatorState{status: RoomRecovering, threadID: image.ThreadID, active: copyBinding(image.Active), pendingControls: append([]TurnBinding(nil), image.PendingControls...), queue: append([]QueuedMessage(nil), image.Queue...), latestSeq: latest}
	return image, nil
}
func (c *Coordinator) setStatus(ctx context.Context, status RoomStatus) error {
	event, err := c.repository.SetRoomStatus(ctx, c.roomID, status)
	if err != nil {
		return err
	}
	if event != nil {
		c.publish(*event)
	}
	c.state.status = status
	return nil
}

func (c *Coordinator) recover(ctx context.Context) error {
	c.threadReady = false
	if c.threadCreationUncertain {
		c.state.status = RoomThreadNeedsRepair
		return ErrDeliveryUnknown
	}
	// Freeze before any fallible read, including after a previous ready cycle.
	c.state.status = RoomRecovering
	c.resetProjection()
	image, err := c.loadRecovery(ctx)
	if err != nil {
		return err
	}
	if image.Status == RoomThreadNeedsRepair {
		c.state.status = RoomThreadNeedsRepair
		return ErrDeliveryUnknown
	}
	// A prior process may have died between thread/start and the binding commit.
	if image.ThreadID == "" && (image.Status == RoomRecovering || image.Active != nil || len(image.PendingControls) != 0) {
		c.markThreadRepair(ctx, "thread-creation-unknown", ErrDeliveryUnknown)
		return ErrDeliveryUnknown
	}
	if err := c.setStatus(ctx, RoomRecovering); err != nil {
		return err
	}
	if c.state.threadID == "" {
		thread, err := c.agent.StartThread(ctx)
		if err != nil {
			if normalizedMutationError(err) == ErrDeliveryNotSent {
				_ = c.setStatus(ctx, RoomReady)
				c.state.status = RoomRecovering
			} else {
				c.markThreadRepair(ctx, "thread-creation-unknown", err)
			}
			return normalizedMutationError(err)
		}
		if !c.validThread(thread, thread.ID) {
			c.markThreadRepair(ctx, "thread-creation-invalid", ErrDeliveryUnknown)
			return ErrDeliveryUnknown
		}
		event, err := c.repository.BindThread(ctx, c.roomID, thread)
		if err != nil {
			c.markThreadRepair(ctx, "thread-bind-unknown", err)
			return ErrDeliveryUnknown
		}
		c.publish(event)
		c.state.threadID = thread.ID
	} else {
		thread, err := c.agent.ReadThread(ctx, c.state.threadID)
		if err != nil {
			c.markThreadRepair(ctx, "thread-read-failed", err)
			return normalizedMutationError(err)
		}
		if !c.validThread(thread, c.state.threadID) {
			c.markThreadRepair(ctx, "thread-mismatch", ErrDeliveryUnknown)
			return ErrDeliveryUnknown
		}
		resumed, err := c.agent.ResumeThread(ctx, c.state.threadID)
		if err != nil {
			return normalizedMutationError(err)
		}
		if !c.validThread(resumed, c.state.threadID) {
			c.markThreadRepair(ctx, "thread-resume-mismatch", ErrDeliveryUnknown)
			return ErrDeliveryUnknown
		}
		if err := c.reconcileHistory(ctx, thread, resumed); err != nil {
			return err
		}
	}
	for i := range c.state.pendingControls {
		control := &c.state.pendingControls[i]
		if control.State == RequestNeedsReview {
			continue
		}
		event, err := c.repository.MarkNeedsReview(ctx, c.roomID, control.MessageID, ReviewReason{Code: "control-delivery-unknown", DetailDigest: digestError(ErrDeliveryUnknown)})
		if err != nil {
			return err
		}
		c.publish(event)
		control.State = RequestNeedsReview
	}
	c.threadReady = true
	if c.state.active == nil && len(c.state.pendingControls) == 0 {
		if err := c.setStatus(ctx, RoomReady); err != nil {
			return err
		}
		return c.dispatchNext(ctx)
	}
	return nil
}

func (c *Coordinator) validThread(thread ThreadSnapshot, id ThreadID) bool {
	if id == "" || thread.ID != id || thread.CWD != c.projectRoot {
		return false
	}
	seen := map[TurnID]bool{}
	for _, turn := range thread.Turns {
		if turn.ID == "" || seen[turn.ID] {
			return false
		}
		seen[turn.ID] = true
		switch turn.State {
		case RequestRunning, RequestCompleted, RequestFailed, RequestInterrupted:
		default:
			return false
		}
		items := map[ItemID]bool{}
		for _, item := range turn.Items {
			if item.ThreadID != id || item.TurnID != turn.ID || item.ItemID == "" || items[item.ItemID] || !json.Valid(item.Payload) {
				return false
			}
			items[item.ItemID] = true
		}
	}
	return true
}
func (c *Coordinator) reconcileHistory(ctx context.Context, histories ...ThreadSnapshot) error {
	terminal := RequestState("")
	for _, history := range histories {
		for _, turn := range history.Turns {
			for _, item := range turn.Items {
				durable, err := c.repository.RecordCompletedItem(ctx, c.roomID, item)
				if err != nil {
					return err
				}
				c.publish(durable)
				// Durable history is independent of the bounded live projection.
				// Only the relevant, still-nonterminal active turn needs suppression.
				if c.state.active != nil && c.state.active.TurnID == turn.ID && !isTerminal(turn.State) && terminal == "" {
					update := c.projection.Apply(AgentEvent{Kind: "item-completed", Completed: &item})
					if update.Transient != nil {
						c.sink.PublishTransient(*update.Transient)
					}
				}
			}
			if c.state.active != nil && c.state.active.TurnID == turn.ID && isTerminal(turn.State) {
				if terminal != "" && terminal != turn.State {
					c.markThreadRepair(ctx, "thread-terminal-conflict", ErrDeliveryUnknown)
					return ErrDeliveryUnknown
				}
				terminal = turn.State
			}
		}
	}
	active := c.state.active
	if active == nil {
		return nil
	}
	if terminal != "" {
		finish := FinishTurnInput{TurnID: active.TurnID, State: terminal}
		if terminal != RequestCompleted {
			finish.ErrorCode = "recovered-" + string(terminal)
		}
		events, err := c.repository.FinishTurn(ctx, c.roomID, finish)
		if err != nil {
			return err
		}
		for _, event := range events {
			c.publish(event)
		}
		for _, event := range c.projection.removeTurn(c.state.threadID, active.TurnID) {
			c.sink.PublishTransient(event)
		}
		c.state.active = nil
		return nil
	}
	if active.State != RequestNeedsReview {
		event, err := c.repository.MarkNeedsReview(ctx, c.roomID, active.MessageID, ReviewReason{Code: "turn-outcome-unproven", DetailDigest: digestError(ErrDeliveryUnknown)})
		if err != nil {
			return err
		}
		c.publish(event)
		active.State = RequestNeedsReview
	}
	return nil
}
func isTerminal(state RequestState) bool {
	return state == RequestCompleted || state == RequestFailed || state == RequestInterrupted
}

func (c *Coordinator) markThreadRepair(ctx context.Context, code string, diagnostic error) {
	c.threadCreationUncertain = true
	c.state.status = RoomThreadNeedsRepair
	if event, err := c.repository.MarkThreadNeedsRepair(ctx, c.roomID, RepairReason{Code: code, DetailDigest: digestError(diagnostic)}); err == nil {
		c.publish(event)
	}
}

func (c *Coordinator) resolve(ctx context.Context, actor Actor, input RecoverInput) error {
	// The current process may still be executing the unsupported turn. A
	// review choice cannot replace proof that its interruption has completed.
	if c.activeFault != "" {
		return ErrDeliveryUnknown
	}
	result, err := c.repository.ResolveReview(ctx, c.roomID, actor, input)
	if err != nil {
		return err
	}
	if result.Duplicate {
		return nil
	}
	c.state.status = RoomRecovering
	for _, event := range result.Events {
		c.publish(event)
	}
	// The review decision is durable, but a failed runtime reconciliation must
	// still complete before either a retry or queued successor can be sent.
	if !c.threadReady {
		return ErrDeliveryUnknown
	}
	image, err := c.loadRecovery(ctx)
	if err != nil {
		return err
	}
	if image.Status == RoomThreadNeedsRepair || c.threadCreationUncertain {
		c.state.status = RoomThreadNeedsRepair
		return ErrDeliveryUnknown
	}
	var retryErr error
	if retry := result.Retry; retry != nil {
		outcome := ControlOutcome{State: RequestFailed, ErrorCode: "stale-turn"}
		retryErr = ErrStaleTurn
		if c.state.active != nil && c.state.active.State == RequestRunning && c.state.active.TurnID == retry.ExpectedTurnID && c.state.threadID != "" {
			switch retry.Kind {
			case RetrySteer:
				retryErr = c.agent.SteerTurn(ctx, c.state.threadID, retry.ExpectedTurnID, retry.Text)
			case RetryCancel:
				retryErr = c.agent.InterruptTurn(ctx, c.state.threadID, retry.ExpectedTurnID)
			default:
				return ErrInvalidRecovery
			}
			outcome = ControlOutcome{State: RequestCompleted}
			if retryErr != nil {
				code, digest, certainty := mutationFailure(retryErr)
				outcome = ControlOutcome{State: RequestFailed, ErrorCode: code, ErrorDigest: digest}
				if certainty == DeliveryUnknown {
					outcome.State = RequestNeedsReview
				}
				retryErr = normalizedMutationError(retryErr)
			}
		}
		event, err := c.repository.FinishControl(ctx, c.roomID, retry.ClientMessageID, outcome)
		if err != nil {
			return err
		}
		c.publish(event)
		if _, err := c.loadRecovery(ctx); err != nil {
			return err
		}
	}
	if len(c.state.pendingControls) == 0 && (c.state.active == nil || c.state.active.State == RequestRunning) {
		if err := c.setStatus(ctx, RoomReady); err != nil {
			return err
		}
		if err := c.dispatchNext(ctx); err != nil {
			return err
		}
	}
	return retryErr
}
