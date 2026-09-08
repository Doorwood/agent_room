package room

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

type countedHistoryRepository struct {
	Repository
	items int
}

func (r *countedHistoryRepository) RecordCompletedItem(ctx context.Context, id RoomID, item CompletedItem) (DurableEvent, error) {
	r.items++
	return r.Repository.RecordCompletedItem(ctx, id, item)
}

func TestLargeTerminalHistoryDoesNotTruncateNextTurn(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("previously-active=%v", active), func(t *testing.T) {
			repo := newTestRepository()
			if active {
				repo.image.Active = &TurnBinding{MessageID: testID(50), TurnID: "historical", State: RequestRunning}
			}
			recorded := &countedHistoryRepository{Repository: repo}
			turn := TurnSnapshot{ID: "historical", State: RequestCompleted}
			for i := 0; i < 4096; i++ {
				turn.Items = append(turn.Items, CompletedItem{ThreadID: "thread-1", TurnID: turn.ID, ItemID: ItemID(fmt.Sprintf("item-%d", i)), Payload: json.RawMessage(`{"text":"done"}`)})
			}
			history := ThreadSnapshot{ID: "thread-1", CWD: "/project", Turns: []TurnSnapshot{turn}}
			resumed := history
			if active {
				// Read may observe a live turn that resume proves terminal.
				history.Turns = append([]TurnSnapshot(nil), history.Turns...)
				history.Turns[0].State = RequestRunning
			}
			agent := &historyAgent{testAgent: newTestAgent(), history: history, resumed: resumed}
			c, stop := startTestCoordinator(t, recorded, agent)
			defer stop()
			snapshot, err := c.Snapshot(context.Background())
			if err != nil || snapshot.Status != RoomReady || snapshot.ProjectionTruncated || len(c.projection.items) != 0 {
				t.Fatalf("terminal history polluted live projection: status=%s truncated=%v keys=%d err=%v", snapshot.Status, snapshot.ProjectionTruncated, len(c.projection.items), err)
			}
			if recorded.items != 8192 {
				t.Fatalf("read/resume history persistence skipped: %d", recorded.items)
			}
			if _, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "new work"}); err != nil {
				t.Fatal(err)
			}
			agent.emit(AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "new-item", Delta: "visible"})
			eventually(t, func() bool {
				snapshot, _ := c.Snapshot(context.Background())
				return !snapshot.ProjectionTruncated && len(snapshot.LiveItems) == 1 && snapshot.LiveItems[0].Partial == "visible"
			})
		})
	}
}

func TestRecoverySuppressionContainsOnlyNonterminalActiveTurn(t *testing.T) {
	repo := newTestRepository()
	repo.image.Active = &TurnBinding{MessageID: testID(1), TurnID: "active", State: RequestRunning}
	history := ThreadSnapshot{ID: "thread-1", CWD: "/project"}
	for _, id := range []TurnID{"old", "active", "unbound"} {
		state := RequestRunning
		if id == "old" {
			state = RequestCompleted
		}
		history.Turns = append(history.Turns, TurnSnapshot{ID: id, State: state, Items: []CompletedItem{{ThreadID: "thread-1", TurnID: id, ItemID: "item-1", Payload: json.RawMessage(`{}`)}}})
	}
	agent := &historyAgent{testAgent: newTestAgent(), history: history, resumed: history}
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	snapshot, err := c.Snapshot(context.Background())
	if err != nil || snapshot.ProjectionTruncated || len(c.projection.items) != 1 || c.projection.Item("active", "item-1").Completed == nil {
		t.Fatalf("incorrect live suppression: truncated=%v keys=%d err=%v", snapshot.ProjectionTruncated, len(c.projection.items), err)
	}
}
