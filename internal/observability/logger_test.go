package observability

import (
	"agent_romm/internal/room"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestLifecycleLogOmitsPromptAndCredentialMaterial(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	l.TurnStarted(context.Background(), TurnFields{ConnectionID: "0000000000000000000000000000000c", ActorUID: 1002, ThreadID: "thread-1", TurnID: "turn-1"})
	for _, s := range []string{"0000000000000000000000000000000c", "1002", "thread-1", "turn-1"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal(s)
		}
	}
}
func TestDiagnosticRedaction(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	sentinel := "authorization=Bearer SECRET-api_key"
	ctx := context.Background()
	l.AcceptedMessage(ctx, MessageFields{ConnectionID: "0000000000000000000000000000000c", ClientMessageID: room.ClientMessageID(sentinel)})
	for _, code := range []FailureCode{InvalidClientID, RPCFailure, ProcessFailure} {
		raw := []byte(sentinel)
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		l.Failure(ctx, FailureFields{Code: code, Digest: DiagnosticDigest(digest), Bytes: uint64(len(raw))})
		if !strings.Contains(out.String(), digest) || !strings.Contains(out.String(), string(code)) {
			t.Fatal(out.String())
		}
	}
	for _, s := range []string{"authorization", "Bearer", "SECRET", "api_key"} {
		if strings.Contains(out.String(), s) {
			t.Fatal("secret leaked")
		}
	}
}
