// Package localprobe provides a loopback-only browser handshake and confirmed joining.
// The public page receives only client version and join state, never its dashboard URL.
package localprobe

import (
	"agent_romm/internal/buildinfo"
	"embed"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"time"
)

const Address = "127.0.0.1:18743"

//go:embed index.html app.js style.css
var assets embed.FS

type Server struct {
	http     *http.Server
	listener net.Listener
}

type JoinRequest struct {
	Address string `json:"address"`
	Session string `json:"session"`
	Name    string `json:"name"`
}
type JoinReply struct {
	State string `json:"state"`
}
type JoinFunc func(JoinRequest, bool) (JoinReply, error)

func Start(dashboardURL string, join ...JoinFunc) (*Server, error) {
	return start(Address, dashboardURL, join...)
}
func start(address, dashboardURL string, join ...JoinFunc) (*Server, error) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	s := &Server{listener: l}
	s.http = &http.Server{Handler: handler(l.Addr().String(), dashboardURL, join...), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
	go s.http.Serve(l)
	return s, nil
}
func (s *Server) Close() error { s.listener.Close(); return s.http.Close() }
func handler(host, dashboardURL string, join ...JoinFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != host {
			http.Error(w, "invalid host", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+host {
			http.Error(w, "invalid origin", 403)
			return
		}
		if r.URL.Path == "/join" || r.URL.Path == "/join-status" {
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			if r.Header.Get("Origin") != "http://"+host {
				http.Error(w, "origin required", 403)
				return
			}
			typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || typ != "application/json" {
				http.Error(w, "JSON required", 415)
				return
			}
			if len(join) == 0 || join[0] == nil {
				http.Error(w, "joining unavailable", 503)
				return
			}
			var request JoinRequest
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			if decoder.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid request", 400)
				return
			}
			reply, err := join[0](request, r.URL.Path == "/join")
			if err != nil {
				http.Error(w, "无法提交申请，请检查地址、Session 和昵称，或打开本机 Dashboard 查看状态", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(reply)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "read only", 405)
			return
		}
		switch r.URL.Path {
		case "/check", "/app.js", "/style.css":
			file, typ := "index.html", "text/html; charset=utf-8"
			if r.URL.Path == "/app.js" {
				file, typ = "app.js", "text/javascript; charset=utf-8"
			}
			if r.URL.Path == "/style.css" {
				file, typ = "style.css", "text/css; charset=utf-8"
			}
			b, _ := assets.ReadFile(file)
			w.Header().Set("Content-Type", typ)
			w.Write(b)
		case "/info":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(struct {
				App     string `json:"app"`
				Version string `json:"version"`
				URL     string `json:"url"`
			}{"agent_room", buildinfo.Version, dashboardURL})
		default:
			http.NotFound(w, r)
		}
	})
}
