package answerwindow

import (
	"agent_romm/internal/network"
	"agent_romm/internal/resources"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResourceGrantConfirmationAndCancellation(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	started := make(chan struct{})
	done := make(chan struct{})
	calls := 0
	w.EnableResources(func(ctx context.Context, r network.ResourceRequest, e resources.Execute) (network.ResourceReply, error) {
		if r.Action == "personal-pending" {
			return network.ResourceReply{Type: "done", Mode: "host", Role: "roommate"}, nil
		}
		calls++
		close(started)
		<-ctx.Done()
		return network.ResourceReply{}, ctx.Err()
	})
	post := func(body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", w.URL()+"resources", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		out := httptest.NewRecorder()
		w.serve(out, r)
		return out
	}
	origin := "http://" + w.listener.Addr().String()
	query := `{"action":"query","mode":"personal","confirmed":true,"resource":{"action":"github.repo","target":"a/b"}}`
	for _, o := range []string{"", "https://evil.test"} {
		if out := post(`{"action":"grant","enabled":true}`, o); out.Code != 403 {
			t.Fatal(out.Code)
		}
	}
	if out := post(query, origin); out.Code != 409 {
		t.Fatal("ungranted request", out.Code)
	}
	if out := post(`{"action":"grant","enabled":true,"token":"secret"}`, origin); out.Code != 400 {
		t.Fatal("accepted token")
	}
	if out := post(`{"action":"grant","enabled":true}`, origin); out.Code != 200 {
		t.Fatal(out.Code)
	}
	if out := post(strings.Replace(query, `"confirmed":true`, `"confirmed":false`, 1), origin); out.Code != 400 {
		t.Fatal("unconfirmed request")
	}
	go func() { defer close(done); post(query, origin) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	if out := post(`{"action":"grant","enabled":false,"root":"invalid-relative-directory"}`, origin); out.Code != 200 {
		t.Fatal("revocation blocked", out.Code)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("not cancelled")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.resourceEnabled || w.resourceRoot != "" {
		t.Fatal("grant remained")
	}
}
func TestResourceWireContainsNoLocalRoot(t *testing.T) {
	b, err := json.Marshal(network.ResourceRequest{Action: "query", Mode: "personal", Resource: resources.Request{Action: "project.file", Target: "README.md"}})
	if err != nil || strings.Contains(string(b), "root") || strings.Contains(string(b), "token") {
		t.Fatal(string(b), err)
	}
}
