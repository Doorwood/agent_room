// Package hostview serves the public, read-only project welcome page.
// It deliberately has no store, coordinator or dashboard management dependency.
package hostview

import (
	"agent_romm/internal/buildinfo"
	"embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"
)

//go:embed index.html app.js style.css
var assets embed.FS

type Info struct {
	Project string `json:"project"`
	Address string `json:"address"`
	Session string `json:"session"`
	Version string `json:"version"`
}
type Server struct {
	listener net.Listener
	http     *http.Server
}

func Start(address string, info Info) (*Server, error) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	info.Version = buildinfo.Version
	s := &Server{listener: l}
	s.http = &http.Server{Handler: Handler(info), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go s.http.Serve(l)
	return s, nil
}
func (s *Server) Address() string { return s.listener.Addr().String() }
func (s *Server) Close() error {
	s.listener.Close()
	err := s.http.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func Handler(info Info) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "只读入口，不支持管理操作", 405)
			return
		}
		if r.URL.Path == "/info" {
			w.Header().Set("Content-Type", "application/json")
			if r.Method != http.MethodHead {
				json.NewEncoder(w).Encode(info)
			}
			return
		}
		file, typ := "", ""
		switch r.URL.Path {
		case "/":
			file, typ = "index.html", "text/html; charset=utf-8"
		case "/app.js":
			file, typ = "app.js", "text/javascript; charset=utf-8"
		case "/style.css":
			file, typ = "style.css", "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile(file)
		w.Header().Set("Content-Type", typ)
		if r.Method != http.MethodHead {
			w.Write(b)
		}
	})
}
