package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"agent_romm/internal/observability"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
)

type observedAgent struct {
	room.Agent
	events <-chan room.AgentEvent
}

func (a observedAgent) StartReadOnlyTurn(ctx context.Context, thread room.ThreadID, id room.ClientMessageID, text string) (room.TurnID, error) {
	agent, ok := a.Agent.(room.ReadOnlyAgent)
	if !ok {
		return "", &room.MutationError{Operation: "turn/start", Certainty: room.DeliveryNotSent, Err: fmt.Errorf("只读问答不可用")}
	}
	return agent.StartReadOnlyTurn(ctx, thread, id, text)
}

func (a observedAgent) Events() <-chan room.AgentEvent { return a.events }
func observeAgent(ctx context.Context, agent room.Agent, log *observability.Logger) (room.Agent, <-chan struct{}) {
	events := make(chan room.AgentEvent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-agent.Events():
				if !ok {
					return
				}
				if event.Error != nil {
					sum := sha256.Sum256([]byte(event.Error.Error()))
					log.Failure(ctx, observability.FailureFields{Code: observability.RPCFailure, Digest: observability.DiagnosticDigest(fmt.Sprintf("%x", sum))})
				}
				select {
				case events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return observedAgent{agent, events}, done
}

type loggedConnections struct {
	*store.Store
	log  *observability.Logger
	uids sync.Map
}

func (c *loggedConnections) Connected(ctx context.Context, record room.ConnectionRecord) error {
	if err := c.Store.Connected(ctx, record); err != nil {
		return err
	}
	c.uids.Store(record.ID, record.UID)
	c.log.Connection(ctx, observability.ConnectionFields{ConnectionID: record.ID, ActorUID: record.UID}, observability.Started)
	return nil
}
func (c *loggedConnections) Disconnected(ctx context.Context, id room.ConnectionID, at time.Time) error {
	err := c.Store.Disconnected(ctx, id, at)
	if uid, ok := c.uids.LoadAndDelete(id); ok {
		c.log.Connection(ctx, observability.ConnectionFields{ConnectionID: id, ActorUID: uid.(room.UID)}, observability.Stopped)
	}
	return err
}
