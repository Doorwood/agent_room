package answerwindow

import (
	"agent_romm/internal/room"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSharedTranscriptIncludesEveryMemberAndNotes(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i, kind := range []string{"message/accepted", "note/accepted"} {
		payload, _ := json.Marshal(map[string]string{"body": "hello", "kind": []string{"prompt", "note"}[i], "client_message_id": "id"})
		if err := w.Event(room.DurableEvent{Seq: room.Seq(i + 1), ActorUID: room.UID(100 + i), Kind: kind, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	w.Members([]room.Member{{UID: 100, Name: "Alice"}, {UID: 101, Name: "Bob"}})
	req := httptest.NewRequest("GET", w.URL()+"answers", nil)
	out := httptest.NewRecorder()
	w.serve(out, req)
	if !strings.Contains(out.Body.String(), "Alice") || !strings.Contains(out.Body.String(), "Bob") || !strings.Contains(out.Body.String(), `"kind":"note"`) {
		t.Fatal(out.Body.String())
	}
}
func TestBrowserSubmissionRequiresSameOriginAndHostConfirmation(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	requests := w.EnableChat()
	body := `{"id":"0123456789abcdef0123456789abcdef","text":"first line\nsecond line"}`
	for _, origin := range []string{"", "https://evil.test"} {
		req := httptest.NewRequest("POST", w.URL()+"submit", strings.NewReader(body))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		w.serve(out, req)
		if out.Code != 403 {
			t.Fatal(out.Code)
		}
	}
	go func() {
		submission := <-requests
		if submission.Text != "first line\nsecond line" {
			submission.Result <- errBadText
			return
		}
		submission.Result <- nil
	}()
	req := httptest.NewRequest("POST", w.URL()+"submit", strings.NewReader(body))
	req.Header.Set("Origin", "http://"+w.listener.Addr().String())
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	w.serve(out, req)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"accepted":true`) {
		t.Fatal(out.Code, out.Body.String())
	}
}

var errBadText = &textError{}

type textError struct{}

func (*textError) Error() string { return "text changed" }
