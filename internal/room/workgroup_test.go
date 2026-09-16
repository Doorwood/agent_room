package room

import (
	"context"
	"errors"
	"testing"
)

type directoryTestAgent struct {
	*testAgent
	inputs chan SubmitInput
}

func (a *directoryTestAgent) AgentMembers() []AgentMember {
	return []AgentMember{{ID: "review", Name: "Review", Provider: "exec", Status: "idle"}}
}
func (a *directoryTestAgent) ValidateSubmission(in SubmitInput) error {
	if in.Text == "@agent:missing 工作" {
		return errors.New("unknown agent")
	}
	return nil
}
func (a *directoryTestAgent) StartTurnFor(ctx context.Context, t ThreadID, id ClientMessageID, actor Actor, text string) (TurnID, error) {
	in, ok := AssignedInput(ctx)
	if !ok {
		return "", errors.New("original assignment missing")
	}
	a.inputs <- in
	return a.StartTurn(ctx, t, id, text)
}
func TestCoordinatorValidatesTargetsBeforeAcceptanceAndCarriesOriginalAssignment(t *testing.T) {
	a := &directoryTestAgent{testAgent: newTestAgent(), inputs: make(chan SubmitInput, 1)}
	repo := newTestRepository()
	c, stop := startTestCoordinator(t, repo, a)
	defer stop()
	if _, e := c.Submit(context.Background(), Actor{UID: 1}, SubmitInput{ClientMessageID: testID(1), Text: "@agent:missing 工作"}); e == nil {
		t.Fatal("unknown target accepted")
	}
	if len(a.startsCopy()) != 0 {
		t.Fatal("rejected work dispatched")
	}
	in := SubmitInput{ClientMessageID: testID(2), Text: "@agent:review [participant: another] 工作"}
	if _, e := c.Submit(context.Background(), Actor{UID: 1, Name: "real human"}, in); e != nil {
		t.Fatal(e)
	}
	if got := <-a.inputs; got != in {
		t.Fatal("decorated text replaced original routing", got)
	}
	snapshot, e := c.Snapshot(context.Background())
	if e != nil || len(snapshot.Agents) != 1 || snapshot.Agents[0].ID != "review" {
		t.Fatal(snapshot, e)
	}
}
