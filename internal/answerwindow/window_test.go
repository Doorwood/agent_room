package answerwindow

import (
	"agent_romm/internal/room"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWindowReadOnlyAndPrivate(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_ = w.Add(room.DurableEvent{Seq: 1}, "<script>alert(1)</script>\n完整回答")
	_ = w.Add(room.DurableEvent{Seq: 1}, "duplicate")
	for _, tc := range []struct {
		path, method, host, origin string
		code                       int
	}{
		{w.path + "answers", "GET", "", "", 200},
		{w.path + "answers?revision=1", "GET", "", "", 204},
		{"/answers", "GET", "", "", 404},
		{w.path + "answers", "POST", "", "", 405},
		{w.path + "answers", "GET", "evil.example", "", 403},
		{w.path + "answers", "GET", "", "https://evil.example", 403},
	} {
		req := httptest.NewRequest(tc.method, "http://"+w.listener.Addr().String()+tc.path, nil)
		if tc.host != "" {
			req.Host = tc.host
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		w.serve(rec, req)
		if rec.Code != tc.code {
			t.Fatalf("%+v: %d", tc, rec.Code)
		}
		if rec.Code == http.StatusOK {
			var state struct {
				Answers []Answer `json:"answers"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
				t.Fatal(err)
			}
			if len(state.Answers) != 1 || !strings.Contains(state.Answers[0].Text, "<script>") {
				t.Fatal(state)
			}
			if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatal("missing CSP")
			}
		}
	}
}
func TestWindowRetainsBoundedHistory(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 1; i <= maxAnswers+10; i++ {
		_ = w.Add(room.DurableEvent{Seq: room.Seq(i)}, "answer")
	}
	if len(w.answers) != maxAnswers || !w.dropped || w.answers[0].Seq != 11 {
		t.Fatal("unbounded history")
	}
}

func TestRoomChangeResetsAnswersAndSameRoomKeepsHistory(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Room("old")
	_ = w.Add(room.DurableEvent{Seq: 1}, "old answer")
	w.Room("old")
	if len(w.answers) != 1 {
		t.Fatal("reconnect lost history")
	}
	w.Room("new")
	_ = w.Add(room.DurableEvent{Seq: 1}, "new answer")
	if len(w.answers) != 1 || w.answers[0].Text != "new answer" {
		t.Fatal(w.answers)
	}
}

type stalledWriter struct {
	header  http.Header
	entered chan struct{}
	resume  chan struct{}
}

func (w *stalledWriter) Header() http.Header { return w.header }
func (w *stalledWriter) WriteHeader(int)     {}
func (w *stalledWriter) Write(b []byte) (int, error) {
	close(w.entered)
	<-w.resume
	return len(b), nil
}
func TestSlowBrowserCannotBlockAnswerDelivery(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	rec := &stalledWriter{header: make(http.Header), entered: make(chan struct{}), resume: make(chan struct{})}
	defer close(rec.resume)
	go w.serve(rec, httptest.NewRequest("GET", w.URL()+"answers", nil))
	<-rec.entered
	done := make(chan struct{})
	go func() { _ = w.Add(room.DurableEvent{Seq: 1}, "new answer"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow browser blocked client")
	}
}
