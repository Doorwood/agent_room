package workgroup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agent_romm/internal/room"
)

type planningAgent struct {
	baseAgent
	reply          string
	planningPrompt string
	writes         int
	denied         bool
}

func (b *planningAgent) StartThread(context.Context) (room.ThreadSnapshot, error) {
	return room.ThreadSnapshot{ID: "planner-isolated"}, nil
}
func (b *planningAgent) StartTurn(context.Context, room.ThreadID, room.ClientMessageID, string) (room.TurnID, error) {
	b.writes++
	return "", nil
}
func (b *planningAgent) PlanCollaboration(_ context.Context, prompt string) (string, error) {
	if b.denied {
		return "", errors.New("隔离规划不可用")
	}
	b.planningPrompt = prompt
	return b.reply, nil
}
func TestNaturalPromptPlansDependenciesInsteadOfMentionOrder(t *testing.T) {
	base := &planningAgent{baseAgent: baseAgent{events: make(chan room.AgentEvent, 8)}, reply: `{"steps":[{"id":"review","agent":"reviewer","prompt":"review completed work","dependsOn":["build"]},{"id":"build","agent":"builder","prompt":"implement"}]}`}
	r, e := New(context.Background(), base, configForTest(), "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	provider := &recordingProvider{}
	r.providers["exec"] = provider
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("9", 32)), Text: "@agent:reviewer @agent:builder reviewer 负责 review，builder 开发登录功能", TaskID: 4}
	_, e = r.StartTurnFor(room.AssignmentContext(context.Background(), in), "main", in.ClientMessageID, room.Actor{UID: 42}, "HOST_PRIVATE_CAPABILITY")
	if e != nil {
		t.Fatal(e)
	}
	if kind := awaitTerminal(t, r); kind != "turn-completed" {
		t.Fatal(kind)
	}
	if base.writes != 0 || !strings.Contains(base.planningPrompt, in.Text) || strings.Contains(base.planningPrompt, "HOST_PRIVATE_CAPABILITY") {
		t.Fatal("planner violated read-only/identity boundary")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.inputs) != 2 || provider.inputs[0].AgentID != "builder" || provider.inputs[1].AgentID != "reviewer" || len(provider.inputs[1].Prior) != 1 || provider.inputs[1].Prior[0].StepID != "build" || provider.inputs[1].HumanUID != 42 {
		t.Fatal(provider.inputs)
	}
}
func TestInvalidOrAmbiguousPlanningDoesNotExecuteWorkers(t *testing.T) {
	for _, reply := range []string{`{"clarification":"评审针对哪个产物？"}`, `not json`, `{"steps":[{"id":"a","agent":"missing","prompt":"do"}]}`, `{"steps":[{"id":"a","agent":"builder","prompt":"do","dependsOn":["b"]},{"id":"b","agent":"reviewer","prompt":"do","dependsOn":["a"]}]}`} {
		t.Run(reply, func(t *testing.T) {
			base := &planningAgent{baseAgent: baseAgent{events: make(chan room.AgentEvent, 8)}, reply: reply}
			r, e := New(context.Background(), base, configForTest(), "/project", t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			p := &recordingProvider{}
			r.providers["exec"] = p
			in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("8", 32)), Text: "让 builder 和 reviewer 协作处理"}
			_, e = r.StartTurnFor(room.AssignmentContext(context.Background(), in), "main", in.ClientMessageID, room.Actor{UID: 42}, in.Text)
			if e != nil {
				t.Fatal(e)
			}
			if kind := awaitTerminal(t, r); kind != "turn-failed" {
				t.Fatal(kind)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if len(p.inputs) != 0 {
				t.Fatal("invalid plan dispatched work")
			}
		})
	}
}
func TestNaturalRoutingAndExplicitCompatibility(t *testing.T) {
	c := configForTest()
	for _, text := range []string{"@agent:reviewer @agent:builder 评审开发结果", "reviewer 做评审，builder 开发", "/team 先开发再评审", "让多个 Agent 协作"} {
		p, e := c.Plan(text)
		if e != nil || !p.Automatic {
			t.Fatal(text, p, e)
		}
	}
	p, e := c.Plan("解释登录函数")
	if e != nil || p.Automatic || len(p.Steps) > 0 {
		t.Fatal(p, e)
	}
}

func TestPlannerPermissionRejectionDoesNotFreezeOrExecute(t *testing.T) {
	base := &planningAgent{baseAgent: baseAgent{events: make(chan room.AgentEvent, 8)}, denied: true}
	r, e := New(context.Background(), base, configForTest(), "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	p := &recordingProvider{}
	r.providers["exec"] = p
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("7", 32)), Text: "/team builder 开发，reviewer 评审"}
	_, e = r.StartTurnFor(room.AssignmentContext(context.Background(), in), "main", in.ClientMessageID, room.Actor{UID: 42}, in.Text)
	if e != nil {
		t.Fatal(e)
	}
	if kind := awaitTerminal(t, r); kind != "turn-failed" {
		t.Fatal(kind)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.inputs) != 0 {
		t.Fatal("permission rejection dispatched work")
	}
}

func TestSingleCodexMentionUsesMainSessionWithoutPlanner(t *testing.T) {
	base := &isolatedCodex{baseAgent: baseAgent{events: make(chan room.AgentEvent, 16)}}
	r, e := New(context.Background(), base, configForTest(), "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("6", 32)), Text: "@agent:codex 修复协作功能"}
	turn, e := r.StartTurnFor(room.AssignmentContext(context.Background(), in), "existing-main", in.ClientMessageID, room.Actor{UID: 42}, "original sender authorization")
	if e != nil || turn != "existing-main-turn" {
		t.Fatal(turn, e)
	}
	if kind := awaitTerminal(t, r); kind != "turn-completed" {
		t.Fatal(kind)
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if len(base.threads) != 0 || len(base.prompts) != 1 || base.prompts[0] != "original sender authorization" {
		t.Fatal("main session or authorization was replaced")
	}
	var n int
	if e := r.db.QueryRow(`SELECT count(*) FROM runs`).Scan(&n); e != nil || n != 0 {
		t.Fatal("single Codex created composite planning run", n, e)
	}
}
func TestSingleOtherAgentRunsWithoutReadOnlyPlanning(t *testing.T) {
	base := &baseAgent{events: make(chan room.AgentEvent, 8)} // no read-only support
	r, e := New(context.Background(), base, configForTest(), "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	provider := &recordingProvider{}
	r.providers["exec"] = provider
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("5", 32)), Text: "@agent:reviewer 检查代码"}
	_, e = r.StartTurnFor(room.AssignmentContext(context.Background(), in), "main", in.ClientMessageID, room.Actor{UID: 42}, in.Text)
	if e != nil {
		t.Fatal(e)
	}
	if kind := awaitTerminal(t, r); kind != "turn-completed" {
		t.Fatal(kind)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.inputs) != 1 || provider.inputs[0].AgentID != "reviewer" {
		t.Fatal(provider.inputs)
	}
}
