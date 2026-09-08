package network

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestUploadRequiresApprovalPersistsBytesAndRejectsRevokedMember(t *testing.T) {
	s, st, _ := testServer(t)
	ctx := context.Background()
	l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: "uploader", Token: strings.Repeat("e", 64)}}
	payload := []byte("\x89PNG\r\n\x1a\nfile bytes")
	if _, err := l.Upload(ctx, "image.png", int64(len(payload)), bytes.NewReader(payload)); err == nil {
		t.Fatal("unapproved upload")
	}
	c, r, err := l.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err = st.DecideJoin(ctx, s.room, r.RequestID, "approve"); err != nil {
		t.Fatal(err)
	}
	file, err := l.Upload(ctx, "../../image.png", int64(len(payload)), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file.Path)
	if err != nil || !bytes.Equal(b, payload) || file.Name != "image.png" {
		t.Fatal(file, err)
	}
	again, err := l.Upload(ctx, "image.png", int64(len(payload)), bytes.NewReader(payload))
	if err != nil || again.Path != file.Path {
		t.Fatal("retry not idempotent", err)
	}
	if _, err = l.Upload(ctx, "big", MaxUploadBytes+1, bytes.NewReader(nil)); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err = st.DecideJoin(ctx, s.room, r.RequestID, "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Upload(ctx, "image.png", int64(len(payload)), bytes.NewReader(payload)); err == nil {
		t.Fatal("revoked upload")
	}
}

func TestUploadChecksumMismatchDoesNotCommit(t *testing.T) {
	s, st, dir := testServer(t)
	ctx := context.Background()
	l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: "checksum", Token: strings.Repeat("d", 64)}}
	c, r, err := l.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err = st.DecideJoin(ctx, s.room, r.RequestID, "approve"); err != nil {
		t.Fatal(err)
	}
	c, _, err = l.dialOperation(ctx, "upload")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = writeJSON(c, uploadRequest{Name: "bad.txt", Size: 3, SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	var result uploadReply
	if err = readJSON(c, &result); err != nil || result.Error != "file checksum mismatch" {
		t.Fatal(result, err)
	}
	// The reply precedes deferred temporary cleanup; no committed target may exist.
	entries, _ := os.ReadDir(dir + "/uploads")
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".upload-") {
			t.Fatal("corrupt file committed")
		}
	}
}
