package room

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPersistedEnumValuesAreStable(t *testing.T) {
	got, err := json.Marshal([]RequestState{
		RequestQueued, RequestDispatching, RequestRunning,
		RequestCompleted, RequestFailed, RequestInterrupted, RequestNeedsReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `["queued","dispatching","running","completed","failed","interrupted","needs-review"]`
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestSubmitInputRejectsBlankText(t *testing.T) {
	err := (SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: " \n"}).Validate()
	if !errors.Is(err, ErrBlankMessage) {
		t.Fatalf("got %v", err)
	}
}

func TestControlInputsValidateMessageAndTurnIDs(t *testing.T) {
	validID := ClientMessageID("00000000000000000000000000000001")
	validTurn := TurnID("turn-1")
	cases := []struct {
		name string
		err  error
	}{
		{"steer message", (SteerInput{ClientMessageID: "bad", ExpectedTurnID: validTurn, Text: "go"}).Validate()},
		{"steer turn", (SteerInput{ClientMessageID: validID, Text: "go"}).Validate()},
		{"steer text", (SteerInput{ClientMessageID: validID, ExpectedTurnID: validTurn, Text: " \t"}).Validate()},
		{"cancel message", (CancelInput{ClientMessageID: "bad", ExpectedTurnID: validTurn}).Validate()},
		{"cancel turn", (CancelInput{ClientMessageID: validID}).Validate()},
		{"control kind", (ControlInput{ClientMessageID: validID, ExpectedTurnID: validTurn, Kind: "unknown"}).Validate()},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
	if err := (ControlInput{ClientMessageID: validID, ExpectedTurnID: validTurn, Kind: ControlSteer, Text: "go"}).Validate(); err != nil {
		t.Fatalf("valid control rejected: %v", err)
	}
}

func TestRecoverInputRequiresActionSpecificFields(t *testing.T) {
	validID := ClientMessageID("00000000000000000000000000000001")
	targetID := ClientMessageID("00000000000000000000000000000002")
	if err := (RecoverInput{ClientMessageID: validID, TargetMessageID: targetID, Action: RecoveryRetry}).Validate(); err != nil {
		t.Fatalf("valid retry rejected: %v", err)
	}
	for _, input := range []RecoverInput{
		{ClientMessageID: "bad", TargetMessageID: targetID, Action: RecoveryRetry},
		{ClientMessageID: validID, TargetMessageID: "bad", Action: RecoveryRetry},
		{ClientMessageID: validID, TargetMessageID: targetID, Action: "unknown"},
		{ClientMessageID: validID, TargetMessageID: targetID, Action: RecoveryRetry, Instruction: "nope"},
		{ClientMessageID: validID, TargetMessageID: targetID, Action: RecoverySkip, ReplacementMessageID: "00000000000000000000000000000003"},
		{ClientMessageID: validID, TargetMessageID: targetID, Action: RecoveryContinue, ReplacementMessageID: validID, Instruction: "continue"},
		{ClientMessageID: validID, TargetMessageID: targetID, Action: RecoveryContinue, ReplacementMessageID: "00000000000000000000000000000003", Instruction: " \n"},
	} {
		if err := input.Validate(); err == nil {
			t.Fatalf("expected validation failure for %#v", input)
		}
	}
	if err := (RecoverInput{ClientMessageID: validID, TargetMessageID: targetID, Action: RecoveryContinue, ReplacementMessageID: "00000000000000000000000000000003", Instruction: "inspect state"}).Validate(); err != nil {
		t.Fatalf("valid continue rejected: %v", err)
	}
}

func TestConnectionIDValidation(t *testing.T) {
	if !ValidConnectionID("0123456789abcdef0123456789abcdef") {
		t.Fatal("expected valid connection ID")
	}
	for _, id := range []ConnectionID{
		"",
		"0123456789abcdef0123456789abcde",
		"0123456789abcdef0123456789abcdef0",
		"0123456789abcdef0123456789ABCDEf",
		"0123456789abcdef0123456789abcde-",
	} {
		if ValidConnectionID(id) {
			t.Fatalf("expected malformed connection ID %q to be rejected", id)
		}
	}
}

func TestConnectionRecordValidate(t *testing.T) {
	record := ConnectionRecord{ID: "0123456789abcdef0123456789abcdef"}
	if err := record.Validate(); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	record.ID = "malformed"
	if err := record.Validate(); !errors.Is(err, ErrInvalidConnectionID) {
		t.Fatalf("got %v, want ErrInvalidConnectionID", err)
	}
}
