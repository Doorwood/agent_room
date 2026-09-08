package room

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestCoordinatorDispatchesFIFOAndLabelsActor(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()

	first, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(context.Background(), Actor{UID: 2, Name: "bob"}, SubmitInput{ClientMessageID: testID(2), Text: "[participant: alice] second"}); err != nil {
		t.Fatal(err)
	}
	if first.State != RequestQueued {
		t.Fatalf("acceptance state=%q", first.State)
	}
	if got := agent.startsCopy(); len(got) != 1 || got[0].text != "[participant: alice]\nfirst" {
		t.Fatalf("starts=%#v", got)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Queue) != 1 || snap.Queue[0].Input.ClientMessageID != testID(2) {
		t.Fatalf("queue=%#v", snap.Queue)
	}
	agent.emit(AgentEvent{Kind: "turn-completed", ThreadID: "thread-1", TurnID: "turn-1"})
	eventually(t, func() bool { return len(agent.startsCopy()) == 2 })
	if got := agent.startsCopy()[1].text; got != "[participant: bob]\n[participant: alice] second" {
		t.Fatalf("text=%q", got)
	}
}

func TestCoordinatorDuplicateAndNoteDoNotDispatch(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	in := SubmitInput{ClientMessageID: testID(1), Text: "inspect"}
	first, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.MessageID != first.MessageID || len(agent.startsCopy()) != 1 {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if _, err := c.Note(context.Background(), Actor{UID: 2, Name: "bob"}, SubmitInput{ClientMessageID: testID(3), Text: "context"}); err != nil {
		t.Fatal(err)
	}
	if len(agent.startsCopy()) != 1 {
		t.Fatal("note dispatched")
	}
}

func TestCoordinatorControlsPersistOutcomesAndDuplicateStale(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	if _, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"}); err != nil {
		t.Fatal(err)
	}
	in := CancelInput{ClientMessageID: testID(2), ExpectedTurnID: "older"}
	got, err := c.Cancel(context.Background(), Actor{UID: 2, Name: "bob"}, in)
	if !errors.Is(err, ErrStaleTurn) || got.ErrorCode != "stale-turn" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	again, err := c.Cancel(context.Background(), Actor{UID: 2, Name: "bob"}, in)
	if !errors.Is(err, ErrStaleTurn) || !again.Duplicate || again.ErrorCode != "stale-turn" {
		t.Fatalf("again=%#v err=%v", again, err)
	}
	if len(agent.interrupts) != 0 || len(repo.controlOutcomes) != 1 {
		t.Fatalf("interrupts=%v outcomes=%#v", agent.interrupts, repo.controlOutcomes)
	}
}

func TestCoordinatorUnknownMutationFreezesQueue(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	agent.startErr = &MutationError{Operation: "start turn", Certainty: DeliveryUnknown, Err: errors.New("secret upstream")}
	_, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(repo.reviews) != 1 || repo.reviews[0].DetailDigest == "" {
		t.Fatalf("reviews=%#v", repo.reviews)
	}
	snap, _ := c.Snapshot(context.Background())
	if snap.Status != RoomRecovering {
		t.Fatalf("status=%q", snap.Status)
	}
}

func TestCoordinatorControlMutationOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantState RequestState
		wantErr   error
	}{
		{name: "success", wantState: RequestCompleted},
		{name: "not sent", err: &MutationError{Operation: "steer", Certainty: DeliveryNotSent, Err: errors.New("private")}, wantState: RequestFailed, wantErr: ErrDeliveryNotSent},
		{name: "unknown", err: &MutationError{Operation: "steer", Certainty: DeliveryUnknown, Err: errors.New("private")}, wantState: RequestNeedsReview, wantErr: ErrDeliveryUnknown},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepository()
			agent := newTestAgent()
			c, stop := startTestCoordinator(t, repo, agent)
			defer stop()
			if _, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"}); err != nil {
				t.Fatal(err)
			}
			agent.steerErr = tt.err
			got, err := c.Steer(context.Background(), Actor{UID: 2, Name: "bob"}, SteerInput{ClientMessageID: testID(i + 4), ExpectedTurnID: "turn-1", Text: "inspect"})
			if !errors.Is(err, tt.wantErr) || got.State != tt.wantState {
				t.Fatalf("got=%#v err=%v", got, err)
			}
			if len(repo.controlOutcomes) != 1 || repo.controlOutcomes[0].State != tt.wantState {
				t.Fatalf("outcomes=%#v", repo.controlOutcomes)
			}
			if tt.err != nil && repo.controlOutcomes[0].ErrorDigest == "private" {
				t.Fatal("raw upstream error persisted")
			}
		})
	}
}

func TestCoordinatorCallerCancellationWithoutRun(t *testing.T) {
	c, err := NewCoordinator("team", "/project", newTestRepository(), newTestAgent(), discardSink{}, testClock{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Snapshot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestCoordinatorUnknownThreadCreationNeverRetries(t *testing.T) {
	repo := newTestRepository()
	repo.image.ThreadID = ""
	agent := newTestAgent()
	agent.startThreadErr = &MutationError{Operation: "start thread", Certainty: DeliveryUnknown, Err: errors.New("lost reply")}
	c, stop := runTestCoordinator(t, repo, agent)
	defer stop()
	if err := c.Recover(context.Background()); !errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("first=%v", err)
	}
	if err := c.Recover(context.Background()); err == nil {
		t.Fatal("second recovery unexpectedly succeeded")
	}
	if agent.startThreadCalls != 1 || repo.image.Status != RoomThreadNeedsRepair {
		t.Fatalf("calls=%d status=%q", agent.startThreadCalls, repo.image.Status)
	}
}

func TestCoordinatorRecoveryFailureNeverLeavesReady(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	agent.readErr = errors.New("offline")
	c, stop := runTestCoordinator(t, repo, agent)
	defer stop()
	if err := c.Recover(context.Background()); err == nil {
		t.Fatal("expected recovery error")
	}
	snap, _ := c.Snapshot(context.Background())
	if snap.Status == RoomReady {
		t.Fatalf("status=%q", snap.Status)
	}
}

func TestCoordinatorFreshControlDuringRecoveryDoesNotCallAgent(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"})
	agent.emit(AgentEvent{Kind: "runtime-unavailable", Error: errors.New("down")})
	eventually(t, func() bool { snap, _ := c.Snapshot(context.Background()); return snap.Status == RoomRecovering })
	_, err := c.Steer(context.Background(), Actor{UID: 2, Name: "bob"}, SteerInput{ClientMessageID: testID(4), ExpectedTurnID: "turn-1", Text: "change"})
	if !errors.Is(err, ErrStaleTurn) || agent.steerCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, agent.steerCalls)
	}
}

func TestCoordinatorUnknownPromptStaysFrozenWhenStatusWriteFails(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	agent.startErr = &MutationError{Operation: "start turn", Certainty: DeliveryUnknown, Err: errors.New("lost")}
	repo.setStatusErr = errors.New("disk")
	_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"})
	agent.startErr = nil
	_, _ = c.Submit(context.Background(), Actor{UID: 2, Name: "bob"}, SubmitInput{ClientMessageID: testID(2), Text: "second"})
	if len(agent.startsCopy()) != 1 {
		t.Fatalf("starts=%#v", agent.startsCopy())
	}
	snap, _ := c.Snapshot(context.Background())
	if snap.Status != RoomRecovering {
		t.Fatalf("status=%q", snap.Status)
	}
}

func TestCoordinatorSnapshotIsIsolated(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "active"})
	_, _ = c.Submit(context.Background(), Actor{UID: 2, Name: "bob"}, SubmitInput{ClientMessageID: testID(2), Text: "queued"})
	snap, _ := c.Snapshot(context.Background())
	snap.Queue[0].Input.Text = "mutated"
	snap.Active.TurnID = "mutated"
	again, _ := c.Snapshot(context.Background())
	if again.Queue[0].Input.Text != "queued" || again.Active.TurnID != "turn-1" {
		t.Fatalf("snapshot=%#v", again)
	}
}

func TestCoordinatorSerializesConcurrentAcceptance(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	var wg sync.WaitGroup
	results := make(chan Acceptance, 3)
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			got, err := c.Submit(context.Background(), Actor{UID: UID(n), Name: "member"}, SubmitInput{ClientMessageID: testID(n), Text: "work"})
			if err != nil {
				t.Error(err)
				return
			}
			results <- got
		}(i)
	}
	wg.Wait()
	close(results)
	seen := map[Seq]bool{}
	ordered := make([]Acceptance, 0, 3)
	for got := range results {
		if seen[got.Seq] {
			t.Fatalf("duplicate seq %d", got.Seq)
		}
		seen[got.Seq] = true
		ordered = append(ordered, got)
	}
	if len(seen) != 3 || len(agent.startsCopy()) != 1 {
		t.Fatalf("seqs=%v starts=%#v", seen, agent.startsCopy())
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	snap, _ := c.Snapshot(context.Background())
	if snap.Active.MessageID != ordered[0].ClientMessageID || snap.Queue[0].Input.ClientMessageID != ordered[1].ClientMessageID || snap.Queue[1].Input.ClientMessageID != ordered[2].ClientMessageID {
		t.Fatalf("ordered=%#v snapshot=%#v", ordered, snap)
	}
}

func TestCoordinatorNotSentFailsDispatch(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	agent.startErr = &MutationError{Operation: "start", Certainty: DeliveryNotSent, Err: errors.New("offline")}
	_, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "work"})
	if !errors.Is(err, ErrDeliveryNotSent) || len(repo.failDispatches) != 1 || repo.failDispatches[0].State != RequestFailed {
		t.Fatalf("err=%v failures=%#v", err, repo.failDispatches)
	}
}

func TestCoordinatorRuntimeUnavailableIsIdempotentAndReadyStaysFrozen(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "work"})
	agent.emit(AgentEvent{Kind: "runtime-unavailable", Error: errors.New("down")})
	agent.emit(AgentEvent{Kind: "runtime-unavailable", Error: errors.New("down")})
	eventually(t, func() bool { return repo.reviewCount() == 1 })
	agent.emit(AgentEvent{Kind: "runtime-ready"})
	time.Sleep(10 * time.Millisecond)
	snap, _ := c.Snapshot(context.Background())
	if snap.Status != RoomRecovering || repo.reviewCount() != 1 {
		t.Fatalf("status=%q reviews=%d", snap.Status, repo.reviewCount())
	}
}

func TestCoordinatorSteerUsesActiveArguments(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "work"})
	_, err := c.Steer(context.Background(), Actor{UID: 2, Name: "bob"}, SteerInput{ClientMessageID: testID(4), ExpectedTurnID: "turn-1", Text: "rotate"})
	if err != nil || len(agent.steers) != 1 || agent.steers[0].thread != "thread-1" || agent.steers[0].turn != "turn-1" || agent.steers[0].text != "rotate" {
		t.Fatalf("err=%v steers=%#v", err, agent.steers)
	}
}

func TestCoordinatorCancellationWhileRepositoryCommandIsAccepted(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	repo.acceptBlock = make(chan struct{})
	repo.acceptEntered = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := c.Submit(ctx, Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "blocked"})
		result <- err
	}()
	select {
	case <-repo.acceptEntered:
	case <-time.After(time.Second):
		t.Fatal("repository was not entered")
	}
	cancel()
	err := <-result
	if !errors.Is(err, context.DeadlineExceeded) {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	}
	close(repo.acceptBlock)
}

func TestCoordinatorBindThreadFailureMarksRepair(t *testing.T) {
	repo := newTestRepository()
	repo.image.ThreadID = ""
	repo.bindThreadErr = errors.New("disk")
	agent := newTestAgent()
	c, stop := runTestCoordinator(t, repo, agent)
	defer stop()
	if err := c.Recover(context.Background()); !errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("err=%v", err)
	}
	if repo.image.Status != RoomThreadNeedsRepair || agent.startThreadCalls != 1 {
		t.Fatalf("status=%q calls=%d", repo.image.Status, agent.startThreadCalls)
	}
}

func TestCoordinatorRecoveryReadFailuresFreezePreviouslyReadyRoom(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*testRepository)
	}{
		{"image", func(r *testRepository) { r.loadErr = errors.New("image read") }},
		{"latest-seq", func(r *testRepository) { r.latestErr = errors.New("seq read") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepository()
			agent := newTestAgent()
			c, stop := startTestCoordinator(t, repo, agent)
			defer stop()
			tc.inject(repo)
			if err := c.Recover(context.Background()); err == nil {
				t.Fatal("expected recovery failure")
			}
			_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "queued"})
			_, _ = c.Steer(context.Background(), Actor{UID: 2, Name: "bob"}, SteerInput{ClientMessageID: testID(4), ExpectedTurnID: "turn-1", Text: "no"})
			if len(agent.startsCopy()) != 0 || agent.steerCalls != 0 {
				t.Fatalf("starts=%#v steers=%d", agent.startsCopy(), agent.steerCalls)
			}
		})
	}
}

func TestCoordinatorRuntimeReadyRetriesAfterFailedRecoveryCycle(t *testing.T) {
	repo := newTestRepository()
	agent := newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	agent.emit(AgentEvent{Kind: "runtime-unavailable", Error: errors.New("down")})
	eventually(t, func() bool { snap, _ := c.Snapshot(context.Background()); return snap.Status == RoomRecovering })
	repo.setLoadErr(errors.New("temporary"))
	agent.emit(AgentEvent{Kind: "runtime-ready"})
	eventually(t, func() bool { return repo.loadCountValue() >= 2 })
	agent.emit(AgentEvent{Kind: "runtime-unavailable", Error: errors.New("down again")})
	repo.setLoadErr(nil)
	agent.emit(AgentEvent{Kind: "runtime-ready"})
	eventually(t, func() bool { snap, _ := c.Snapshot(context.Background()); return snap.Status == RoomReady })
}

func TestCoordinatorPersistenceFaultsPreserveUncertaintyAcrossRecover(t *testing.T) {
	t.Run("prompt review write", func(t *testing.T) {
		repo := newTestRepository()
		agent := newTestAgent()
		c, stop := startTestCoordinator(t, repo, agent)
		defer stop()
		repo.markReviewErr = errors.New("disk")
		agent.startErr = &MutationError{Operation: "start", Certainty: DeliveryUnknown, Err: errors.New("lost")}
		_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"})
		agent.startErr = nil
		_ = c.Recover(context.Background())
		_, _ = c.Submit(context.Background(), Actor{UID: 2, Name: "bob"}, SubmitInput{ClientMessageID: testID(2), Text: "second"})
		if len(agent.startsCopy()) != 1 {
			t.Fatalf("starts=%#v", agent.startsCopy())
		}
	})
	t.Run("control finish write", func(t *testing.T) {
		repo := newTestRepository()
		agent := newTestAgent()
		c, stop := startTestCoordinator(t, repo, agent)
		defer stop()
		_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"})
		repo.finishControlErr = errors.New("disk")
		agent.steerErr = &MutationError{Operation: "steer", Certainty: DeliveryUnknown, Err: errors.New("lost")}
		_, _ = c.Steer(context.Background(), Actor{UID: 2, Name: "bob"}, SteerInput{ClientMessageID: testID(4), ExpectedTurnID: "turn-1", Text: "change"})
		_ = c.Recover(context.Background())
		_, _ = c.Submit(context.Background(), Actor{UID: 3, Name: "carol"}, SubmitInput{ClientMessageID: testID(2), Text: "second"})
		if len(agent.startsCopy()) != 1 {
			t.Fatalf("starts=%#v", agent.startsCopy())
		}
	})
	t.Run("repair write", func(t *testing.T) {
		repo := newTestRepository()
		repo.image.ThreadID = ""
		repo.repairErr = errors.New("disk")
		agent := newTestAgent()
		agent.startThreadErr = &MutationError{Operation: "create", Certainty: DeliveryUnknown, Err: errors.New("lost")}
		c, stop := runTestCoordinator(t, repo, agent)
		defer stop()
		_ = c.Recover(context.Background())
		_ = c.Recover(context.Background())
		if agent.startThreadCalls != 1 {
			t.Fatalf("calls=%d", agent.startThreadCalls)
		}
	})
}

type testRepository struct {
	mu               sync.Mutex
	next             int64
	accepted         map[ClientMessageID]Acceptance
	image            RecoveryImage
	controlOutcomes  []ControlOutcome
	reviews          []ReviewReason
	setStatusErr     error
	failDispatches   []FailureOutcome
	acceptBlock      chan struct{}
	bindThreadErr    error
	loadErr          error
	latestErr        error
	loadCount        int
	acceptEntered    chan struct{}
	markReviewErr    error
	finishControlErr error
	repairErr        error
}

func (r *testRepository) reviewCount() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.reviews) }

func newTestRepository() *testRepository {
	return &testRepository{accepted: map[ClientMessageID]Acceptance{}, image: RecoveryImage{Status: RoomReady, ThreadID: "thread-1"}}
}
func (r *testRepository) LoadRecoveryImage(context.Context, RoomID) (RecoveryImage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadCount++
	return r.image, r.loadErr
}
func (r *testRepository) loadCountValue() int  { r.mu.Lock(); defer r.mu.Unlock(); return r.loadCount }
func (r *testRepository) setLoadErr(err error) { r.mu.Lock(); defer r.mu.Unlock(); r.loadErr = err }
func (r *testRepository) FindMember(context.Context, RoomID, UID) (Member, error) {
	return Member{}, nil
}
func (r *testRepository) accept(id ClientMessageID) Acceptance {
	if a, ok := r.accepted[id]; ok {
		a.Duplicate = true
		return a
	}
	r.next++
	a := Acceptance{MessageID: r.next, ClientMessageID: id, Seq: Seq(r.next), State: RequestQueued, Event: DurableEvent{Seq: Seq(r.next)}}
	r.accepted[id] = a
	return a
}
func (r *testRepository) AcceptMessage(_ context.Context, _ RoomID, _ Actor, in SubmitInput) (Acceptance, error) {
	if r.acceptEntered != nil {
		select {
		case r.acceptEntered <- struct{}{}:
		default:
		}
	}
	if r.acceptBlock != nil {
		<-r.acceptBlock
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accept(in.ClientMessageID), nil
}
func (r *testRepository) AppendNote(_ context.Context, _ RoomID, _ Actor, in SubmitInput) (Acceptance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.accept(in.ClientMessageID)
	a.State = RequestCompleted
	return a, nil
}
func (r *testRepository) AcceptControl(_ context.Context, _ RoomID, _ Actor, in ControlInput) (Acceptance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.accept(in.ClientMessageID)
	if !a.Duplicate {
		r.image.PendingControls = append(r.image.PendingControls, TurnBinding{MessageID: in.ClientMessageID, TurnID: in.ExpectedTurnID, State: RequestDispatching})
	}
	return a, nil
}
func (r *testRepository) FinishControl(_ context.Context, _ RoomID, id ClientMessageID, o ControlOutcome) (DurableEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finishControlErr != nil {
		return DurableEvent{}, r.finishControlErr
	}
	r.controlOutcomes = append(r.controlOutcomes, o)
	a := r.accepted[id]
	a.State = o.State
	a.ErrorCode = o.ErrorCode
	r.accepted[id] = a
	for i := range r.image.PendingControls {
		if r.image.PendingControls[i].MessageID == id {
			if o.State == RequestNeedsReview {
				r.image.PendingControls[i].State = o.State
			} else {
				r.image.PendingControls = append(r.image.PendingControls[:i], r.image.PendingControls[i+1:]...)
			}
			break
		}
	}
	return DurableEvent{Seq: a.Seq}, nil
}
func (r *testRepository) BeginDispatch(_ context.Context, _ RoomID, id ClientMessageID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.image.Active = &TurnBinding{MessageID: id, State: RequestDispatching}
	return nil
}

func (r *testRepository) FailDispatch(_ context.Context, _ RoomID, _ ClientMessageID, o FailureOutcome) (DurableEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failDispatches = append(r.failDispatches, o)
	return DurableEvent{}, nil
}
func (r *testRepository) BindRunningTurn(_ context.Context, _ RoomID, id ClientMessageID, turn TurnID) (DurableEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.image.Active = &TurnBinding{MessageID: id, TurnID: turn, State: RequestRunning}
	return DurableEvent{}, nil
}
func (r *testRepository) RecordCompletedItem(context.Context, RoomID, CompletedItem) (DurableEvent, error) {
	return DurableEvent{}, nil
}
func (r *testRepository) FinishTurn(_ context.Context, _ RoomID, _ FinishTurnInput) ([]DurableEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.image.Active = nil
	return nil, nil
}
func (r *testRepository) MarkNeedsReview(_ context.Context, _ RoomID, _ ClientMessageID, x ReviewReason) (DurableEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.markReviewErr != nil {
		return DurableEvent{}, r.markReviewErr
	}
	r.reviews = append(r.reviews, x)
	if r.image.Active != nil {
		r.image.Active.State = RequestNeedsReview
	}
	return DurableEvent{}, nil
}
func (r *testRepository) ResolveReview(context.Context, RoomID, Actor, RecoverInput) (RecoveryResult, error) {
	return RecoveryResult{}, nil
}
func (r *testRepository) BindThread(context.Context, RoomID, ThreadSnapshot) (DurableEvent, error) {
	return DurableEvent{}, r.bindThreadErr
}
func (r *testRepository) MarkThreadNeedsRepair(context.Context, RoomID, RepairReason) (DurableEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repairErr != nil {
		return DurableEvent{}, r.repairErr
	}
	r.image.Status = RoomThreadNeedsRepair
	return DurableEvent{}, nil
}
func (r *testRepository) SetRoomStatus(_ context.Context, _ RoomID, s RoomStatus) (*DurableEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setStatusErr != nil {
		return nil, r.setStatusErr
	}
	r.image.Status = s
	return &DurableEvent{}, nil
}
func (r *testRepository) SaveRuntimeCheckpoint(context.Context, RoomID, RuntimeCheckpoint) error {
	return nil
}
func (r *testRepository) LatestSeq(context.Context, RoomID) (Seq, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Seq(r.next), r.latestErr
}
func (r *testRepository) Events(context.Context, RoomID, Seq, Seq, int) ([]DurableEvent, error) {
	return nil, nil
}

type startCall struct {
	thread ThreadID
	id     ClientMessageID
	text   string
}
type steerCall struct {
	thread ThreadID
	turn   TurnID
	text   string
}
type testAgent struct {
	mu               sync.Mutex
	events           chan AgentEvent
	starts           []startCall
	interrupts       []TurnID
	startErr         error
	steerErr         error
	startThreadErr   error
	readErr          error
	startThreadCalls int
	steerCalls       int
	steers           []steerCall
}

func newTestAgent() *testAgent { return &testAgent{events: make(chan AgentEvent, 10)} }
func (a *testAgent) StartThread(context.Context) (ThreadSnapshot, error) {
	a.startThreadCalls++
	return ThreadSnapshot{ID: "thread-new", CWD: "/project"}, a.startThreadErr
}
func (a *testAgent) ReadThread(context.Context, ThreadID) (ThreadSnapshot, error) {
	return ThreadSnapshot{ID: "thread-1", CWD: "/project"}, a.readErr
}
func (a *testAgent) ResumeThread(context.Context, ThreadID) (ThreadSnapshot, error) {
	return ThreadSnapshot{ID: "thread-1", CWD: "/project"}, nil
}
func (a *testAgent) StartTurn(_ context.Context, t ThreadID, id ClientMessageID, text string) (TurnID, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts = append(a.starts, startCall{t, id, text})
	if a.startErr != nil {
		return "", a.startErr
	}
	return TurnID("turn-" + string(rune('0'+len(a.starts)))), nil
}
func (a *testAgent) SteerTurn(_ context.Context, thread ThreadID, turn TurnID, text string) error {
	a.steerCalls++
	a.steers = append(a.steers, steerCall{thread, turn, text})
	return a.steerErr
}
func (a *testAgent) InterruptTurn(_ context.Context, _ ThreadID, t TurnID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.interrupts = append(a.interrupts, t)
	return nil
}
func (a *testAgent) Events() <-chan AgentEvent { return a.events }
func (a *testAgent) emit(e AgentEvent)         { a.events <- e }
func (a *testAgent) startsCopy() []startCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]startCall(nil), a.starts...)
}

type discardSink struct{}

func (discardSink) PublishDurable(DurableEvent)     {}
func (discardSink) PublishTransient(TransientEvent) {}

type testClock struct{}

func (testClock) Now() time.Time { return time.Unix(1, 0) }
func startTestCoordinator(t *testing.T, r Repository, a Agent) (*Coordinator, func()) {
	t.Helper()
	c, err := NewCoordinator("team", "/project", r, a, discardSink{}, testClock{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	if err := c.Recover(context.Background()); err != nil {
		cancel()
		t.Fatal(err)
	}
	return c, func() { cancel(); <-done }
}
func runTestCoordinator(t *testing.T, r Repository, a Agent) (*Coordinator, func()) {
	t.Helper()
	c, err := NewCoordinator("team", "/project", r, a, discardSink{}, testClock{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	return c, func() { cancel(); <-done }
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met")
}
func testID(n int) ClientMessageID {
	return ClientMessageID("0000000000000000000000000000000" + string(rune('0'+n)))
}
