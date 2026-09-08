package answerwindow

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCancelBindsActiveTurnAndWaitsForHost(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	submissions := w.EnableChat()
	w.Room("room")
	w.Connection(true)
	w.ActiveTurn("turn-current")
	send := func(turn, origin string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"id": strings.Repeat("a", 32), "expectedTurn": turn})
		r := httptest.NewRequest("POST", w.URL()+"cancel", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		out := httptest.NewRecorder()
		w.serve(out, r)
		return out
	}
	origin := "http://" + w.listener.Addr().String()
	if out := send("turn-old", origin); out.Code != 409 {
		t.Fatal(out.Code)
	}
	if out := send("turn-current", "https://evil.example"); out.Code != 403 {
		t.Fatal(out.Code)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- send("turn-current", origin) }()
	select {
	case submission := <-submissions:
		if submission.Method != "cancel" || submission.ExpectedTurnID != "turn-current" {
			t.Fatal(submission)
		}
		select {
		case <-done:
			t.Fatal("ack before host")
		default:
		}
		submission.Result <- nil
	case <-time.After(time.Second):
		t.Fatal("no stop submission")
	}
	if out := <-done; out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	w.ActiveTurn("turn-next")
	if out := send("turn-current", origin); out.Code != 409 {
		t.Fatal("stale stop affects next task", out.Code)
	}
	w.Connection(false)
	if out := send("turn-next", origin); out.Code != 409 {
		t.Fatal(out.Code)
	}
}
