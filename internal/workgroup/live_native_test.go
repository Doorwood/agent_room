package workgroup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: consumes the execution machine's Cursor account.
func TestLiveCursorReadOnlyFixture(t *testing.T) {
	if os.Getenv("AGENT_ROOM_LIVE_CURSOR_TEST") != "1" {
		t.Skip("opt-in live Cursor test")
	}
	root := t.TempDir()
	marker := "ROOM_CURSOR_CHECK_84721"
	os.WriteFile(filepath.Join(root, "proof.txt"), []byte(marker), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	r, e := (NativeProvider{Provider: "cursor"}).Run(ctx, Member{ID: "cursor", Mode: "review"}, Assignment{ProjectRoot: root, Prompt: "Read proof.txt in the current directory and reply only with its exact contents. Do not edit files or use network tools."})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(r.Text, marker) {
		t.Fatal("fixture was not read", r.Text)
	}
	after, _ := os.ReadFile(filepath.Join(root, "proof.txt"))
	if string(after) != marker {
		t.Fatal("read-only fixture changed")
	}
	t.Log("Cursor returned the exact local file marker")
}
