package answerwindow

import (
	"agent_romm/internal/gitpush"
	"agent_romm/internal/network"
	"agent_romm/internal/personal"
	"agent_romm/internal/resources"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonalPushNeedsItsOwnExactAccountConfirmation(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.DraftDirectory(filepath.Join(t.TempDir(), "drafts"))
	pack := make([]byte, 32)
	sum := sha256.Sum256(pack)
	o := gitpush.Offer{Input: gitpush.Input{Repository: "alice/project", Branch: "main", Commit: strings.Repeat("a", 40)}, PackSize: len(pack), PackSHA256: hex.EncodeToString(sum[:])}
	p := &personal.Request{ID: strings.Repeat("a", 32), Action: "git.push", Sender: "alice", Push: &o}
	chunks := 0
	w.personalClient = strings.Repeat("c", 32)
	w.resource = func(_ context.Context, r network.ResourceRequest, _ resources.Execute) (network.ResourceReply, error) {
		reply := network.ResourceReply{Type: "done", Mode: "personal", Role: "roommate", Personal: p}
		if r.Action == "personal-push-chunk" {
			chunks++
			reply.PushData = pack
		}
		return reply, nil
	}
	w.personalAccount = "ou_otheraccount123"
	w.personalGit = &personal.GitIdentity{Name: "Alice", Email: "alice@example.test"}
	w.personalStep(context.Background())
	if chunks != 0 {
		t.Fatal("downloaded before separate push approval")
	}
	u, _ := url.Parse(w.URL())
	post := func(body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", w.URL()+"personal", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		out := httptest.NewRecorder()
		w.servePersonal(out, r)
		return out
	}
	origin := "http://" + u.Host
	body := `{"action":"push-grant","id":"` + p.ID + `","githubId":42}`
	if r := post(body, "http://evil.test"); r.Code != 403 {
		t.Fatal(r.Code)
	}
	if r := post(body, origin); r.Code != 409 {
		t.Fatal("granted without account check", r.Code)
	}
	w.personalPush = &gitpush.Approval{ID: p.ID, Offer: o, Account: gitpush.Account{ID: 42, Login: "alice"}}
	if r := post(strings.Replace(body, "42", "43", 1), origin); r.Code != 409 {
		t.Fatal("wrong account accepted", r.Code)
	}
	if r := post(strings.Replace(body, "push-grant", "grant", 1), origin); r.Code != 400 {
		t.Fatal("Feishu grant accepted for push", r.Code)
	}
	if r := post(body, origin); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	w.personalStep(context.Background())
	if chunks != 1 {
		t.Fatal("did not continue after approval", chunks)
	}
	// The test approval deliberately has no credential: execution refuses it and requires a new check.
	if w.personalPush != nil || w.personalPushGranted != "" {
		t.Fatal("failed approval reused")
	}
	w.personalStep(context.Background())
	if chunks != 1 {
		t.Fatal("automatic retry after error")
	}
	r := post(`{"action":"push-history"}`, origin)
	var history []gitpush.Receipt
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &history) != nil || len(history) != 0 {
		t.Fatal("unexpected receipt", r.Body.String())
	}
}
