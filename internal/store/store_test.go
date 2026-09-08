package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent_romm/internal/room"
)

const (
	aliceID room.ClientMessageID = "00000000000000000000000000000001"
	bobID   room.ClientMessageID = "00000000000000000000000000000002"
	thirdID room.ClientMessageID = "00000000000000000000000000000003"
	digest                       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var (
	alice = room.Actor{UID: 1001, Name: "alice"}
	bob   = room.Actor{UID: 1002, Name: "bob"}
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "room.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s
}

func seedRoom(t *testing.T, s *Store) {
	t.Helper()
	err := s.InitializeRoom(context.Background(), RoomSeed{
		ID:                "team",
		DisplayName:       "Team",
		HostID:            "host-1",
		ProjectRoot:       "/srv/project",
		ExecutionOwnerUID: alice.UID,
		Members: []room.Member{
			{UID: alice.UID, Name: alice.Name},
			{UID: bob.UID, Name: bob.Name},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func acceptedPrompt(t *testing.T, s *Store, id room.ClientMessageID) room.Acceptance {
	t.Helper()
	got, err := s.AcceptMessage(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: id, Text: "inspect auth"})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func beginAndReview(t *testing.T, s *Store, id room.ClientMessageID) {
	t.Helper()
	acceptedPrompt(t, s, id)
	if err := s.BeginDispatch(context.Background(), "team", id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkNeedsReview(context.Background(), "team", id, room.ReviewReason{Code: "delivery-unknown", DetailDigest: digest}); err != nil {
		t.Fatal(err)
	}
}

func tableCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func messageState(t *testing.T, s *Store, id room.ClientMessageID) (room.RequestState, string) {
	t.Helper()
	var state room.RequestState
	var code *string
	if err := s.db.QueryRowContext(context.Background(), `
		SELECT m.state, b.error_code
		FROM messages m LEFT JOIN turn_bindings b ON b.message_id = m.id
		WHERE m.room_id = ? AND m.client_message_id = ?`, "team", id).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if code == nil {
		return state, ""
	}
	return state, *code
}

func TestOpenConfiguresSQLiteAndInitializeRoomIsOneShot(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)

	var foreignKeys, busyTimeout int
	var journalMode string
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
		t.Fatalf("foreign_keys=%d busy_timeout=%d journal_mode=%q", foreignKeys, busyTimeout, journalMode)
	}
	if got := s.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections=%d", got)
	}
	if err := s.InitializeRoom(context.Background(), RoomSeed{ID: "other", DisplayName: "Other", HostID: "host", ProjectRoot: "/tmp", ExecutionOwnerUID: 3, Members: []room.Member{{UID: 3, Name: "other"}}}); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("got %v", err)
	}
	var version, migrationCount int
	if err := s.db.QueryRow("SELECT MAX(version), count(*) FROM schema_migrations").Scan(&version, &migrationCount); err != nil {
		t.Fatal(err)
	}
	if version != 2 || migrationCount != 2 {
		t.Fatalf("schema version=%d migration count=%d", version, migrationCount)
	}
}

func TestEveryPhysicalConnectionGetsPragmasAndLiteralSpecialCharacterPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "room ?# state.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seedRoom(t, s)

	// Discard Open's physical connection. The next Conn must be freshly opened
	// from the configured DSN, not inherit connection-local state by accident.
	s.db.SetMaxIdleConns(0)
	connection, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var foreignKeys, busyTimeout int
	var journalMode string
	if err := connection.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
		t.Fatalf("foreign_keys=%d busy_timeout=%d journal_mode=%q", foreignKeys, busyTimeout, journalMode)
	}
	_, err = connection.ExecContext(context.Background(), `
		INSERT INTO client_connections(id, room_id, member_uid, connected_at, last_ack_seq)
		VALUES ('ffffffffffffffffffffffffffffffff', 'team', 9999, '2026-09-03T00:00:00Z', 0)`)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("new connection did not enforce member FK: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("special-character path was not treated literally: %v", err)
	}
	if got := s.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections=%d", got)
	}
}

func TestInitializeRoomRejectsInvalidAndDuplicateMembersAtomically(t *testing.T) {
	cases := []RoomSeed{
		{ID: "", DisplayName: "Team", HostID: "host", ProjectRoot: "/srv", ExecutionOwnerUID: 1, Members: []room.Member{{UID: 1, Name: "a"}}},
		{ID: "team", DisplayName: "", HostID: "host", ProjectRoot: "/srv", ExecutionOwnerUID: 1, Members: []room.Member{{UID: 1, Name: "a"}}},
		{ID: "team", DisplayName: "Team", HostID: "host", ProjectRoot: "/srv", ExecutionOwnerUID: 1, Members: []room.Member{{UID: 1, Name: "a"}, {UID: 1, Name: "b"}}},
		{ID: "team", DisplayName: "Team", HostID: "host", ProjectRoot: "/srv", ExecutionOwnerUID: 9, Members: []room.Member{{UID: 1, Name: "a"}}},
	}
	for index, seed := range cases {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			s := openTestStore(t)
			if err := s.InitializeRoom(context.Background(), seed); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got %v", err)
			}
			if got := tableCount(t, s, "rooms"); got != 0 {
				t.Fatalf("rooms=%d", got)
			}
		})
	}
}

func TestFindMemberRequiresExistingRoomMember(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	got, err := s.FindMember(context.Background(), "team", alice.UID)
	if err != nil || got != (room.Member{UID: alice.UID, Name: alice.Name}) {
		t.Fatalf("member=%#v err=%v", got, err)
	}
	if _, err := s.FindMember(context.Background(), "team", 9999); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestAcceptMessageIsIdempotentAndAtomic(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	in := room.SubmitInput{ClientMessageID: aliceID, Text: "inspect auth"}
	first, err := s.AcceptMessage(context.Background(), "team", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AcceptMessage(context.Background(), "team", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !second.Duplicate || second.MessageID != first.MessageID || second.Seq != first.Seq || second.State != room.RequestQueued {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if tableCount(t, s, "messages") != 1 || tableCount(t, s, "turn_bindings") != 1 || tableCount(t, s, "room_events") != 1 {
		t.Fatal("acceptance rows were not exactly-once")
	}
}

func TestAcceptanceIdentityHasNoAmbiguousConcatenation(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	_, err := s.AcceptMessage(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "23"})
	if err != nil {
		t.Fatal(err)
	}
	// A length-delimited identity must not confuse actor 1/body "23" with actor 12/body "3".
	if _, err := s.AcceptMessage(context.Background(), "team", room.Actor{UID: 12, Name: "nobody"}, room.SubmitInput{ClientMessageID: aliceID, Text: "3"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("got %v", err)
	}
}

func TestAcceptanceRejectsInvalidActorInputAndEveryIdentityChange(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	if _, err := s.AcceptMessage(context.Background(), "team", room.Actor{UID: alice.UID, Name: "mallory"}, room.SubmitInput{ClientMessageID: aliceID, Text: "one"}); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("actor mismatch: %v", err)
	}
	if _, err := s.AcceptMessage(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: "bad", Text: "one"}); !errors.Is(err, room.ErrInvalidClientMessageID) {
		t.Fatalf("validation: %v", err)
	}
	if _, err := s.AcceptMessage(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "one"}); err != nil {
		t.Fatal(err)
	}
	calls := []func() error{
		func() error {
			_, err := s.AcceptMessage(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "two"})
			return err
		},
		func() error {
			_, err := s.AcceptMessage(context.Background(), "team", bob, room.SubmitInput{ClientMessageID: aliceID, Text: "one"})
			return err
		},
		func() error {
			_, err := s.AppendNote(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "one"})
			return err
		},
		func() error {
			_, err := s.AcceptControl(context.Background(), "team", alice, room.ControlInput{ClientMessageID: aliceID, Kind: room.ControlSteer, ExpectedTurnID: "turn-1", Text: "one"})
			return err
		},
	}
	for index, call := range calls {
		if err := call(); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("case %d got %v", index, err)
		}
	}
	if tableCount(t, s, "messages") != 1 || tableCount(t, s, "room_events") != 1 {
		t.Fatal("conflict mutated original records")
	}
}

func TestAppendNoteIsTerminalIdempotentAndHasNoBinding(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	in := room.SubmitInput{ClientMessageID: aliceID, Text: "human context"}
	first, err := s.AppendNote(context.Background(), "team", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AppendNote(context.Background(), "team", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != room.RequestCompleted || !second.Duplicate || second.State != room.RequestCompleted || tableCount(t, s, "turn_bindings") != 0 {
		t.Fatalf("first=%#v second=%#v bindings=%d", first, second, tableCount(t, s, "turn_bindings"))
	}
}

func TestAcceptControlCoversSteerCancelDuplicateAndConflicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   room.ControlInput
	}{
		{"steer", room.ControlInput{ClientMessageID: aliceID, Kind: room.ControlSteer, ExpectedTurnID: "turn-1", Text: "look here"}},
		{"cancel", room.ControlInput{ClientMessageID: aliceID, Kind: room.ControlCancel, ExpectedTurnID: "turn-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			first, err := s.AcceptControl(context.Background(), "team", alice, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.AcceptControl(context.Background(), "team", alice, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if first.State != room.RequestDispatching || !second.Duplicate || second.State != room.RequestDispatching {
				t.Fatalf("first=%#v second=%#v", first, second)
			}
			variants := []room.ControlInput{
				{ClientMessageID: aliceID, Kind: room.ControlSteer, ExpectedTurnID: "turn-2", Text: "look here"},
				{ClientMessageID: aliceID, Kind: room.ControlSteer, ExpectedTurnID: "turn-1", Text: "changed"},
				{ClientMessageID: aliceID, Kind: room.ControlCancel, ExpectedTurnID: "turn-1"},
			}
			for _, variant := range variants {
				if variant == tc.in {
					continue
				}
				if _, err := s.AcceptControl(context.Background(), "team", alice, variant); !errors.Is(err, ErrIdempotencyConflict) {
					t.Fatalf("variant=%#v err=%v", variant, err)
				}
			}
		})
	}
}

func TestConcurrentDuplicateHasOneWinner(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	start := make(chan struct{})
	results := make(chan room.Acceptance, 16)
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := s.AcceptMessage(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "one"})
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("accept: %v", err)
	}
	var messageID int64
	var winners int
	for got := range results {
		if messageID == 0 {
			messageID = got.MessageID
		}
		if got.MessageID != messageID || got.Seq != 1 {
			t.Fatalf("different acceptance: %#v", got)
		}
		if !got.Duplicate {
			winners++
		}
	}
	if winners != 1 || tableCount(t, s, "messages") != 1 || tableCount(t, s, "turn_bindings") != 1 || tableCount(t, s, "room_events") != 1 {
		t.Fatalf("winners=%d messages=%d bindings=%d events=%d", winners, tableCount(t, s, "messages"), tableCount(t, s, "turn_bindings"), tableCount(t, s, "room_events"))
	}
}

func TestBeforeCommitFailureRollsBackEveryAcceptanceWriteAndSequence(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	injected := errors.New("injected before commit")
	s.beforeCommit = func() error { return injected }
	_, err := s.AcceptMessage(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "one"})
	if !errors.Is(err, injected) {
		t.Fatalf("got %v", err)
	}
	s.beforeCommit = nil
	if tableCount(t, s, "messages") != 0 || tableCount(t, s, "turn_bindings") != 0 || tableCount(t, s, "room_events") != 0 {
		t.Fatal("partial acceptance survived rollback")
	}
	got, err := s.AcceptMessage(context.Background(), "team", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != 1 {
		t.Fatalf("rolled-back sequence was consumed: %d", got.Seq)
	}
}

func TestDuplicateAcceptanceDoesNotInvokeTheWriteCommitHook(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	in := room.SubmitInput{ClientMessageID: aliceID, Text: "one"}
	first, err := s.AcceptMessage(context.Background(), "team", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	s.beforeCommit = func() error { return errors.New("write hook must not run") }
	duplicate, err := s.AcceptMessage(context.Background(), "team", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.MessageID != first.MessageID || duplicate.Seq != first.Seq {
		t.Fatalf("first=%#v duplicate=%#v", first, duplicate)
	}
}

func TestDispatchAndTurnTransitionsUseStrictOldStatePredicates(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	if _, err := s.BindThread(context.Background(), "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}); err != nil {
		t.Fatal(err)
	}
	acceptedPrompt(t, s, aliceID)
	if err := s.BeginDispatch(context.Background(), "team", aliceID); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginDispatch(context.Background(), "team", aliceID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second begin got %v", err)
	}
	running, err := s.BindRunningTurn(context.Background(), "team", aliceID, "turn-1")
	if err != nil || running.Kind != "turn/running" {
		t.Fatalf("event=%#v err=%v", running, err)
	}
	if _, err := s.BindRunningTurn(context.Background(), "team", aliceID, "turn-2"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second bind got %v", err)
	}
	item := room.CompletedItem{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Payload: json.RawMessage(`{"text":"done"}`)}
	itemEvent, err := s.RecordCompletedItem(context.Background(), "team", item)
	if err != nil || itemEvent.Kind != "item/completed" {
		t.Fatalf("event=%#v err=%v", itemEvent, err)
	}
	events, err := s.FinishTurn(context.Background(), "team", room.FinishTurnInput{TurnID: "turn-1", State: room.RequestCompleted})
	if err != nil || len(events) != 1 || events[0].Kind != "turn/completed" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if _, err := s.FinishTurn(context.Background(), "team", room.FinishTurnInput{TurnID: "turn-1", State: room.RequestCompleted}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("late finish got %v", err)
	}
	state, _ := messageState(t, s, aliceID)
	if state != room.RequestCompleted {
		t.Fatalf("state=%s", state)
	}
}

func TestRecordCompletedItemRejectsAStaleThreadWithoutAnEvent(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	if _, err := s.BindThread(context.Background(), "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}); err != nil {
		t.Fatal(err)
	}
	acceptedPrompt(t, s, aliceID)
	if err := s.BeginDispatch(context.Background(), "team", aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindRunningTurn(context.Background(), "team", aliceID, "turn-1"); err != nil {
		t.Fatal(err)
	}
	before := tableCount(t, s, "room_events")
	_, err := s.RecordCompletedItem(context.Background(), "team", room.CompletedItem{
		ThreadID: "thread-from-old-adapter",
		TurnID:   "turn-1",
		ItemID:   "item-1",
		Payload:  json.RawMessage(`{"text":"late"}`),
	})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("got %v", err)
	}
	if got := tableCount(t, s, "room_events"); got != before {
		t.Fatalf("stale item appended an event: before=%d after=%d", before, got)
	}
}

func TestFailDispatchAndFinishControlReturnStableDuplicateState(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	acceptedPrompt(t, s, aliceID)
	if err := s.BeginDispatch(context.Background(), "team", aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailDispatch(context.Background(), "team", aliceID, room.FailureOutcome{State: room.RequestFailed, ErrorCode: "not-sent", ErrorDigest: digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailDispatch(context.Background(), "team", aliceID, room.FailureOutcome{State: room.RequestFailed, ErrorCode: "not-sent", ErrorDigest: digest}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second fail got %v", err)
	}

	control := room.ControlInput{ClientMessageID: bobID, Kind: room.ControlSteer, ExpectedTurnID: "turn-1", Text: "check"}
	if _, err := s.AcceptControl(context.Background(), "team", bob, control); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishControl(context.Background(), "team", bobID, room.ControlOutcome{State: room.RequestFailed, ErrorCode: "stale-turn"}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.AcceptControl(context.Background(), "team", bob, control)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.State != room.RequestFailed || duplicate.ErrorCode != "stale-turn" {
		t.Fatalf("duplicate=%#v", duplicate)
	}
	if _, err := s.FinishControl(context.Background(), "team", bobID, room.ControlOutcome{State: room.RequestCompleted}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second finish got %v", err)
	}
}

func TestFinishControlRejectsTurnOnlyInterruptedState(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	in := room.ControlInput{ClientMessageID: aliceID, Kind: room.ControlCancel, ExpectedTurnID: "turn-1"}
	if _, err := s.AcceptControl(context.Background(), "team", alice, in); err != nil {
		t.Fatal(err)
	}
	_, err := s.FinishControl(context.Background(), "team", aliceID, room.ControlOutcome{State: room.RequestInterrupted, ErrorCode: "interrupted"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
	state, _ := messageState(t, s, aliceID)
	if state != room.RequestDispatching {
		t.Fatalf("invalid outcome mutated state to %s", state)
	}
}

func TestTransitionValidationRejectsBadEnumsDigestsAndRawErrorCodes(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	acceptedPrompt(t, s, aliceID)
	if err := s.BeginDispatch(context.Background(), "team", aliceID); err != nil {
		t.Fatal(err)
	}
	cases := []room.FailureOutcome{
		{State: room.RequestCompleted},
		{State: room.RequestState("mystery"), ErrorCode: "bad", ErrorDigest: digest},
		{State: room.RequestFailed, ErrorCode: "rpc failed: secret token", ErrorDigest: digest},
		{State: room.RequestFailed, ErrorCode: "not-sent", ErrorDigest: "ABC"},
	}
	for _, outcome := range cases {
		if _, err := s.FailDispatch(context.Background(), "team", aliceID, outcome); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("outcome=%#v err=%v", outcome, err)
		}
	}
	state, _ := messageState(t, s, aliceID)
	if state != room.RequestDispatching {
		t.Fatalf("invalid input mutated state to %s", state)
	}
}

func TestEventsAreMonotonicRangedAndPayloadCopiesAreSafe(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	acceptedPrompt(t, s, aliceID)
	if _, err := s.AppendNote(context.Background(), "team", bob, room.SubmitInput{ClientMessageID: bobID, Text: "note"}); err != nil {
		t.Fatal(err)
	}
	acceptedPrompt(t, s, thirdID)
	if got, err := s.LatestSeq(context.Background(), "team"); err != nil || got != 3 {
		t.Fatalf("latest=%d err=%v", got, err)
	}
	events, err := s.Events(context.Background(), "team", 1, 3, 1)
	if err != nil || len(events) != 1 || events[0].Seq != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	events[0].Payload[0] = 'x'
	again, err := s.Events(context.Background(), "team", 1, 0, 10)
	if err != nil || len(again) != 2 || again[0].Seq != 2 || again[1].Seq != 3 || !json.Valid(again[0].Payload) {
		t.Fatalf("events=%#v err=%v", again, err)
	}
	if _, err := s.Events(context.Background(), "team", 3, 2, 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid range got %v", err)
	}
}

func TestConnectionLifecycleRequiresMembershipAndMonotonicAcks(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	when := time.Date(2026, 9, 3, 10, 11, 12, 123456789, time.FixedZone("offset", 8*60*60))
	record := room.ConnectionRecord{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RoomID: "team", UID: alice.UID, ConnectedAt: when}
	if err := s.Connected(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack(context.Background(), record.ID, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack(context.Background(), record.ID, 4); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("decreasing ack got %v", err)
	}
	if err := s.Ack(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("unknown ack got %v", err)
	}
	if err := s.Connected(context.Background(), room.ConnectionRecord{ID: "cccccccccccccccccccccccccccccccc", RoomID: "team", UID: 9999, ConnectedAt: when}); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("nonmember got %v", err)
	}
	if err := s.Connected(context.Background(), room.ConnectionRecord{ID: "bad", RoomID: "team", UID: alice.UID, ConnectedAt: when}); !errors.Is(err, room.ErrInvalidConnectionID) {
		t.Fatalf("bad id got %v", err)
	}
	closedAt := when.Add(time.Minute)
	if err := s.Disconnected(context.Background(), record.ID, closedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.Disconnected(context.Background(), record.ID, closedAt.Add(time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second disconnect got %v", err)
	}
	var storedConnected, storedDisconnected string
	var ack room.Seq
	if err := s.db.QueryRow("SELECT connected_at, disconnected_at, last_ack_seq FROM client_connections WHERE id = ?", record.ID).Scan(&storedConnected, &storedDisconnected, &ack); err != nil {
		t.Fatal(err)
	}
	if storedConnected != "2026-09-03T02:11:12.123456789Z" || storedDisconnected != "2026-09-03T02:12:12.123456789Z" || ack != 5 {
		t.Fatalf("connected=%q disconnected=%q ack=%d", storedConnected, storedDisconnected, ack)
	}
}

func TestDisconnectedRejectsATimestampBeforeConnection(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	connectedAt := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	id := room.ConnectionID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := s.Connected(context.Background(), room.ConnectionRecord{ID: id, RoomID: "team", UID: alice.UID, ConnectedAt: connectedAt}); err != nil {
		t.Fatal(err)
	}
	if err := s.Disconnected(context.Background(), id, connectedAt.Add(-time.Nanosecond)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
	var disconnected any
	if err := s.db.QueryRow("SELECT disconnected_at FROM client_connections WHERE id = ?", id).Scan(&disconnected); err != nil {
		t.Fatal(err)
	}
	if disconnected != nil {
		t.Fatalf("connection closed at invalid time: %v", disconnected)
	}
}

func TestCloseStaleOnlyClosesOnlineConnections(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	now := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	one := room.ConnectionRecord{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RoomID: "team", UID: alice.UID, ConnectedAt: now}
	two := room.ConnectionRecord{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RoomID: "team", UID: bob.UID, ConnectedAt: now}
	if err := s.Connected(context.Background(), one); err != nil {
		t.Fatal(err)
	}
	if err := s.Connected(context.Background(), two); err != nil {
		t.Fatal(err)
	}
	if err := s.Disconnected(context.Background(), one.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseStale(context.Background(), "team", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var oneAt, twoAt string
	if err := s.db.QueryRow("SELECT disconnected_at FROM client_connections WHERE id = ?", one.ID).Scan(&oneAt); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT disconnected_at FROM client_connections WHERE id = ?", two.ID).Scan(&twoAt); err != nil {
		t.Fatal(err)
	}
	if oneAt != "2026-09-03T03:01:00Z" || twoAt != "2026-09-03T03:02:00Z" {
		t.Fatalf("one=%q two=%q", oneAt, twoAt)
	}
}

func TestCloseStaleRejectsConnectionNewerThanRecoveryTime(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	recoveryAt := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	id := room.ConnectionID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := s.Connected(context.Background(), room.ConnectionRecord{ID: id, RoomID: "team", UID: alice.UID, ConnectedAt: recoveryAt.Add(time.Nanosecond)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseStale(context.Background(), "team", recoveryAt); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
	var disconnected any
	if err := s.db.QueryRow("SELECT disconnected_at FROM client_connections WHERE id = ?", id).Scan(&disconnected); err != nil {
		t.Fatal(err)
	}
	if disconnected != nil {
		t.Fatalf("future connection was closed: %v", disconnected)
	}
}

func TestCloseStaleMixedBatchRollsBackEveryConnection(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	recoveryAt := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	records := []room.ConnectionRecord{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RoomID: "team", UID: alice.UID, ConnectedAt: recoveryAt.Add(-time.Minute)},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RoomID: "team", UID: bob.UID, ConnectedAt: recoveryAt.Add(time.Nanosecond)},
	}
	for _, record := range records {
		if err := s.Connected(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseStale(context.Background(), "team", recoveryAt); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
	var online int
	if err := s.db.QueryRow("SELECT count(*) FROM client_connections WHERE room_id = ? AND disconnected_at IS NULL", "team").Scan(&online); err != nil {
		t.Fatal(err)
	}
	if online != 2 {
		t.Fatalf("mixed batch partially closed: online=%d", online)
	}
}

func TestCloseStaleReadsEntireBatchAndPrioritizesCorruptStoredTime(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	recoveryAt := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	records := []room.ConnectionRecord{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RoomID: "team", UID: alice.UID, ConnectedAt: recoveryAt.Add(time.Second)},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RoomID: "team", UID: bob.UID, ConnectedAt: recoveryAt.Add(-time.Second)},
	}
	for _, record := range records {
		if err := s.Connected(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	corruptValue := "corrupt-secret-timestamp"
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO client_connections(id, room_id, member_uid, connected_at, last_ack_seq)
		VALUES (?, 'team', ?, ?, 0)`, "cccccccccccccccccccccccccccccccc", alice.UID, corruptValue); err != nil {
		t.Fatal(err)
	}
	err := s.CloseStale(context.Background(), "team", recoveryAt)
	if err == nil || errors.Is(err, ErrInvalidInput) || err.Error() != "corrupt store data: invalid connection timestamp" {
		t.Fatalf("corrupt timestamp was masked by chronology error: %v", err)
	}
	if strings.Contains(err.Error(), corruptValue) {
		t.Fatalf("stored timestamp leaked through error: %v", err)
	}
	var online int
	if err := s.db.QueryRow("SELECT count(*) FROM client_connections WHERE room_id = 'team' AND disconnected_at IS NULL").Scan(&online); err != nil {
		t.Fatal(err)
	}
	if online != 3 {
		t.Fatalf("failed batch partially closed: online=%d", online)
	}
}

func TestCompositeForeignKeysRejectCrossRoomMessageAssociations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		insert func(context.Context, *Store, int64, int64) error
	}{
		{
			name: "turn-binding",
			insert: func(ctx context.Context, s *Store, teamMessageID, otherMessageID int64) error {
				_, err := s.db.ExecContext(ctx, "INSERT INTO turn_bindings(room_id, message_id, state) VALUES (?, ?, ?)", "team", otherMessageID, room.RequestQueued)
				return err
			},
		},
		{
			name: "recovery-command",
			insert: func(ctx context.Context, s *Store, teamMessageID, otherMessageID int64) error {
				_, err := s.db.ExecContext(ctx, "INSERT INTO recovery_results(recovery_message_id, room_id, target_message_id, action) VALUES (?, ?, ?, ?)", teamMessageID, "other", otherMessageID, room.RecoverySkip)
				return err
			},
		},
		{
			name: "recovery-target",
			insert: func(ctx context.Context, s *Store, teamMessageID, otherMessageID int64) error {
				_, err := s.db.ExecContext(ctx, "INSERT INTO recovery_results(recovery_message_id, room_id, target_message_id, action) VALUES (?, ?, ?, ?)", otherMessageID, "other", teamMessageID, room.RecoverySkip)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			team := acceptedPrompt(t, s, aliceID)
			otherMessageID := insertOtherRoomMessage(t, s)
			if err := tc.insert(context.Background(), s, team.MessageID, otherMessageID); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				t.Fatalf("cross-room association accepted: %v", err)
			}
		})
	}
}

func insertOtherRoomMessage(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO rooms(id, display_name, host_id, project_path, execution_owner_uid, status, schema_version)
		VALUES ('other', 'Other', 'host-2', '/srv/other', 2001, 'ready', ?)`, schemaVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO members(room_id, uid, username, added_at) VALUES ('other', 2001, 'other', '2026-09-03T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO messages(room_id, client_message_id, actor_uid, kind, body, payload_hash, state, accepted_seq, created_at)
		VALUES ('other', '99999999999999999999999999999999', 2001, 'prompt', 'other', ?, 'queued', 1, '2026-09-03T00:00:00Z')`, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPublicMethodsRejectUnknownRoomsWithoutCreatingState(t *testing.T) {
	s := openTestStore(t)
	calls := []func() error{
		func() error {
			_, err := s.AcceptMessage(context.Background(), "missing", alice, room.SubmitInput{ClientMessageID: aliceID, Text: "x"})
			return err
		},
		func() error { _, err := s.LatestSeq(context.Background(), "missing"); return err },
		func() error { _, err := s.LoadRecoveryImage(context.Background(), "missing"); return err },
		func() error { return s.CloseStale(context.Background(), "missing", time.Now()) },
	}
	for index, call := range calls {
		if err := call(); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("case %d got %v", index, err)
		}
	}
	if tableCount(t, s, "rooms") != 0 {
		t.Fatal("unknown room operation created a room")
	}
}

func TestDurablePayloadsNeverContainRawDiagnostics(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	acceptedPrompt(t, s, aliceID)
	if err := s.BeginDispatch(context.Background(), "team", aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailDispatch(context.Background(), "team", aliceID, room.FailureOutcome{State: room.RequestFailed, ErrorCode: "rpc-eof", ErrorDigest: digest}); err != nil {
		t.Fatal(err)
	}
	var bindingCode, bindingDigest string
	if err := s.db.QueryRow("SELECT error_code, error_digest FROM turn_bindings").Scan(&bindingCode, &bindingDigest); err != nil {
		t.Fatal(err)
	}
	var payloads string
	rows, err := s.db.Query("SELECT payload FROM room_events")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		payloads += string(payload)
	}
	if bindingCode != "rpc-eof" || bindingDigest != digest || strings.Contains(payloads, "secret stderr") {
		t.Fatalf("code=%q digest=%q payloads=%q", bindingCode, bindingDigest, payloads)
	}

	acceptedPrompt(t, s, bobID)
	if err := s.BeginDispatch(context.Background(), "team", bobID); err != nil {
		t.Fatal(err)
	}
	raw := "rpc failed: secret stderr"
	if _, err := s.FailDispatch(context.Background(), "team", bobID, room.FailureOutcome{State: room.RequestFailed, ErrorCode: raw, ErrorDigest: digest}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("raw diagnostic got %v", err)
	}
	var leaked int
	if err := s.db.QueryRow(`
		SELECT
		  (SELECT count(*) FROM turn_bindings WHERE instr(COALESCE(error_code, ''), ?) > 0) +
		  (SELECT count(*) FROM room_events WHERE instr(CAST(payload AS TEXT), ?) > 0) +
		  (SELECT count(*) FROM recovery_results WHERE instr(action, ?) > 0)`, raw, raw, raw).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("raw diagnostic leaked into %d durable rows", leaked)
	}
}
