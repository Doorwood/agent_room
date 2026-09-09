package hostview

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicViewHasOnlyReadOnlyRoutes(t *testing.T) {
	info := Info{Project: "demo", Address: "192.0.2.1:7443", Session: "public-session", Version: "test"}
	handler := Handler(info)
	for _, path := range []string{"/", "/info", "/app.js", "/style.css"} {
		r := httptest.NewRequest("GET", "http://192.0.2.1:7444"+path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("unexpected CORS")
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("missing frame policy")
		}
	}
	for _, path := range []string{"/", "/info", "/rooms", "/connect", "/approve", "/submit", "/tasks", "/upload", "/disconnect", "/delete"} {
		for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(method, "http://192.0.2.1:7444"+path, strings.NewReader(`{"action":"approve"}`)))
			if w.Code != 405 {
				t.Fatal(method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/rooms", "/history", "/questions", "/private/config.json", "/network-admin.sock", "/../../README.md"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://192.0.2.1:7444"+path, nil))
		if w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "http://192.0.2.1:7444/info", nil))
	var fields map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil || len(fields) != 4 || fields["project"] != "demo" {
		t.Fatal(fields, err)
	}
}

func TestPublicListenerClosesEvenBeforeServeStarts(t *testing.T) {
	for i := 0; i < 5; i++ {
		s, err := Start("127.0.0.1:0", Info{Project: "demo"})
		if err != nil {
			t.Fatal(err)
		}
		address := s.Address()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		next, err := Start(address, Info{})
		if err != nil {
			t.Fatal("listener still bound", err)
		}
		next.Close()
	}
}
