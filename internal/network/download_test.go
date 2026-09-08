package network

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadRequiresMembershipAndReturnsExactUploadedBytes(t *testing.T) {
	s, st, _ := testServer(t)
	ctx := context.Background()
	l := Launcher{Credential{Address: s.Address(), Session: s.SessionID(), Name: "files", Token: strings.Repeat("b", 64)}}
	c, r, err := l.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err = st.DecideJoin(ctx, s.room, r.RequestID, "approve"); err != nil {
		t.Fatal(err)
	}
	payload := []byte("persisted file bytes")
	file, err := l.Upload(ctx, "report.txt", int64(len(payload)), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	result, err := l.Download(ctx, filepath.Base(file.Path), &got)
	if err != nil || !bytes.Equal(got.Bytes(), payload) || result.Name != "report.txt" {
		t.Fatal(result, err)
	}
	if _, err = l.Download(ctx, "../../config.json", &got); err == nil {
		t.Fatal("path traversal")
	}
	if _, err = st.DecideJoin(ctx, s.room, r.RequestID, "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Download(ctx, filepath.Base(file.Path), &got); err == nil {
		t.Fatal("revoked read")
	}
}
