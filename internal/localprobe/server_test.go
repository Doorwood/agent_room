package localprobe

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeRejectsForeignOriginsHostsAndWrites(t *testing.T) {
	h := handler(Address, "http://127.0.0.1:12345/private-dashboard/")
	for _, tc := range []struct {
		method, host, origin, path string
		code                       int
	}{
		{"GET", Address, "", "/info", 200},
		{"GET", Address, "http://" + Address, "/info", 200},
		{"GET", Address, "http://192.0.2.1:7444", "/info", 403},
		{"GET", "evil.test:18743", "", "/info", 403},
		{"POST", Address, "http://" + Address, "/info", 405},
		{"GET", Address, "", "/rooms", 404},
		{"GET", Address, "", "/connect", 404},
	} {
		r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(tc, w.Code)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("private URL must not be readable cross-origin")
		}
		if tc.code != 200 && strings.Contains(w.Body.String(), "private-dashboard") {
			t.Fatal("leaked local capability")
		}
	}
}
func TestDiscoveryCanBeClosedAndRestarted(t *testing.T) {
	s, err := start("127.0.0.1:0", "http://127.0.0.1:12345/private/")
	if err != nil {
		t.Fatal(err)
	}
	addr := s.listener.Addr().String()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = start(addr, "http://127.0.0.1:12345/private/")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestJoinRequiresSameOriginExplicitJSONPost(t *testing.T) {
	calls := 0
	h := handler(Address, "http://127.0.0.1:123/private/", func(r JoinRequest, submit bool) (JoinReply, error) {
		calls++
		return JoinReply{State: "pending"}, nil
	})
	for _, tc := range []struct {
		method, origin, typ, body string
		code                      int
	}{
		{"GET", "", "", "", 405},
		{"POST", "", "application/json", "{}", 403},
		{"POST", "http://evil.test", "application/json", "{}", 403},
		{"POST", "http://" + Address, "text/plain", "{}", 415},
		{"POST", "http://" + Address, "application/json", `{"token":"bad"}`, 400},
		{"POST", "http://" + Address, "application/json", "{}{}", 400},
		{"POST", "http://" + Address, "application/json", "{}", 200},
	} {
		before := calls
		r := httptest.NewRequest(tc.method, "http://"+Address+"/join", strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.typ)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
		if tc.code != 200 && calls != before {
			t.Fatal("untrusted request called join")
		}
		if tc.code == 200 && strings.TrimSpace(w.Body.String()) != `{"state":"pending"}` {
			t.Fatal("unexpected response", w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
