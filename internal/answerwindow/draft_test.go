package answerwindow

import (
	"bytes"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDraftPersistsAcrossWindowsButNotIdentities(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	a, _ := Start()
	a.DraftDirectory(dir)
	a.Metadata("host", "session", "alice")
	req := httptest.NewRequest("POST", a.URL()+"draft", bytes.NewBufferString(`{"text":"keep my work"}`))
	req.Header.Set("Origin", "http://"+a.listener.Addr().String())
	out := httptest.NewRecorder()
	a.serve(out, req)
	if out.Code != 200 {
		t.Fatal(out.Code)
	}
	a.Close()
	b, _ := Start()
	defer b.Close()
	b.DraftDirectory(dir)
	b.Metadata("host", "session", "alice")
	out = httptest.NewRecorder()
	b.serve(out, httptest.NewRequest("GET", b.URL()+"draft", nil))
	if !bytes.Contains(out.Body.Bytes(), []byte("keep my work")) {
		t.Fatal("lost draft")
	}
	b.Metadata("host", "session", "bob")
	out = httptest.NewRecorder()
	b.serve(out, httptest.NewRequest("GET", b.URL()+"draft", nil))
	if out.Body.String() != "{}" {
		t.Fatal("cross-identity draft")
	}
}
