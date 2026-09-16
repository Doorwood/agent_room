package cli

import (
	"context"
	"testing"

	"agent_romm/internal/room"
)

type isolatedPlannerStub struct {
	room.Agent
	received string
}

func (a *isolatedPlannerStub) PlanCollaboration(_ context.Context, prompt string) (string, error) {
	a.received = prompt
	return `{"steps":[]}`, nil
}
func TestObservedAgentPreservesIsolatedPlanner(t *testing.T) {
	base := &isolatedPlannerStub{}
	wrapped := observedAgent{Agent: base}
	var planner room.CollaborationPlanner = wrapped
	reply, err := planner.PlanCollaboration(context.Background(), "public roster and request")
	if err != nil || reply != `{"steps":[]}` || base.received != "public roster and request" {
		t.Fatal(reply, err)
	}
	if _, err := (observedAgent{}).PlanCollaboration(context.Background(), "request"); err == nil {
		t.Fatal("missing planner accepted")
	}
}
