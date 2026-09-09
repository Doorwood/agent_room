package answerwindow

import (
	"agent_romm/internal/network"
	"agent_romm/internal/personal"
	"agent_romm/internal/resources"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomaticCreationRequiresLocalGrantAndRechecksAccount(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "identity.json")
	callsPath := filepath.Join(dir, "calls")
	t.Setenv("PERSONAL_TEST_IDENTITY", identityPath)
	t.Setenv("PERSONAL_TEST_CALLS", callsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
if [ "$1" = "whoami" ]; then cat "$PERSONAL_TEST_IDENTITY"; exit; fi
if [ "$1" != "docs" ] || [ "$2" != "+create" ]; then exit 1; fi
cat >/dev/null
printf x >> "$PERSONAL_TEST_CALLS"
echo '{"ok":true,"identity":"user","data":{"document":{"document_id":"LocalDoc123456","url":"https://example.feishu.cn/docx/LocalDoc123456"}}}'
`
	if err := os.WriteFile(filepath.Join(dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	setIdentity := func(id string) {
		b, _ := json.Marshal(map[string]any{"identity": "user", "available": true, "tokenStatus": "ready", "onBehalfOf": map[string]string{"userName": "Local User", "openId": id}})
		if err := os.WriteFile(identityPath, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	setIdentity("ou_alice123456")
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.DraftDirectory(filepath.Join(dir, "drafts"))
	p := &personal.Request{ID: strings.Repeat("a", 32), Title: "Plan", Content: "Body", Sender: "alice"}
	var result *personal.Result
	w.personalClient = strings.Repeat("c", 32)
	w.resource = func(ctx context.Context, r network.ResourceRequest, _ resources.Execute) (network.ResourceReply, error) {
		if r.Action == "personal-result" {
			result = r.PersonalResult
			p = nil
		}
		return network.ResourceReply{Type: "done", Mode: "personal", Role: "roommate", Personal: p}, nil
	}
	w.personalStep(context.Background())
	if w.personalPending == nil || result != nil {
		t.Fatal("did not wait for consent")
	}
	if _, err := os.Stat(callsPath); !os.IsNotExist(err) {
		t.Fatal("executed before grant")
	}
	post := func(body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", w.URL()+"personal", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		out := httptest.NewRecorder()
		w.serve(out, r)
		return out
	}
	origin := "http://" + w.listener.Addr().String()
	grant := `{"action":"grant","id":"` + p.ID + `","accountId":"ou_alice123456"}`
	if out := post(grant, ""); out.Code != 403 {
		t.Fatal("missing origin allowed", out.Code)
	}
	if out := post(grant, origin); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	setIdentity("ou_bob12345678")
	w.personalStep(context.Background())
	if w.personalAccount != "" || result != nil {
		t.Fatal("changed account executed")
	}
	if _, err := os.Stat(callsPath); !os.IsNotExist(err) {
		t.Fatal("created as changed login")
	}
	if out := post(grant, origin); out.Code != 409 {
		t.Fatal("old account granted")
	}
	if out := post(strings.Replace(grant, "ou_alice123456", "ou_bob12345678", 1), origin); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	w.personalStep(context.Background())
	if result == nil || result.Receipt == nil || result.Receipt.Account.OpenID != "ou_bob12345678" {
		t.Fatal(result)
	}
	calls, _ := os.ReadFile(callsPath)
	if string(calls) != "x" {
		t.Fatal("write count", string(calls))
	}
}
