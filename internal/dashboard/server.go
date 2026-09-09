package dashboard

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent_romm/internal/buildinfo"
	"agent_romm/internal/client"
	"agent_romm/internal/localprobe"
	"agent_romm/internal/network"
)

//go:embed index.html app.js style.css
var assets embed.FS

type connection struct {
	room   Room
	cancel context.CancelFunc
	done   chan struct{}
}
type Server struct {
	probe     *localprobe.Server
	workers   sync.WaitGroup
	catalog   Catalog
	connector Connector
	mu        sync.Mutex
	active    map[string]*connection
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	listener  net.Listener
	http      *http.Server
	path      string
}

func Start(ctx context.Context, catalog Catalog, connector Connector) (*Server, error) {
	if connector == nil {
		connector = catalog.Connect
	}
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{catalog: catalog, connector: connector, active: map[string]*connection{}, listener: l, path: "/" + hex.EncodeToString(token[:]) + "/", ctx: ctx, cancel: cancel}
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go s.http.Serve(l)
	// Discovery is optional; a busy probe port must not prevent local management.
	s.probe, _ = localprobe.Start(s.URL(), s.joinFromBrowser)
	return s, nil
}

// joinFromBrowser keeps credentials and management URLs exclusively on this machine.
func (s *Server) joinFromBrowser(request localprobe.JoinRequest, submit bool) (localprobe.JoinReply, error) {
	address, err := network.Address(strings.TrimSpace(request.Address))
	if err != nil {
		return localprobe.JoinReply{}, err
	}
	session, name := strings.TrimSpace(request.Session), strings.TrimSpace(request.Name)
	if submit {
		room, err := s.catalog.Add(address, session, name)
		if err != nil {
			return localprobe.JoinReply{}, err
		}
		if err := s.connect(room); err != nil {
			return localprobe.JoinReply{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.active[identity(address, session, name)]; c != nil {
		return localprobe.JoinReply{State: c.room.Status}, nil
	}
	return localprobe.JoinReply{State: "disconnected"}, nil
}
func (s *Server) URL() string { return "http://" + s.listener.Addr().String() + s.path }
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	var done []chan struct{}
	for _, c := range s.active {
		if c.cancel != nil {
			c.cancel()
		}
		done = append(done, c.done)
	}
	s.mu.Unlock()
	if s.probe != nil {
		s.probe.Close()
	}
	err := s.http.Close()
	for _, d := range done {
		<-d
	}
	s.workers.Wait()
	return err
}
func (s *Server) connect(r Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catalog.hidden(r.ID) {
		return errors.New("room 已删除，请重新添加")
	}
	if s.closed {
		return errors.New("dashboard 已关闭")
	}
	if c := s.active[r.ID]; c != nil && c.cancel != nil {
		return nil
	}
	running := 0
	for _, c := range s.active {
		if c.cancel != nil {
			running++
		}
	}
	if running >= 16 {
		return errors.New("最多同时连接 16 个 room，请先断开一个连接")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	r.Status = "connecting"
	r.Detail = "正在连接 host"
	r.URL = ""
	c := &connection{room: r, cancel: cancel, done: make(chan struct{})}
	s.active[r.ID] = c
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer close(c.done)
		update := func(status, detail, url string) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if ctx.Err() == nil {
				c.room.Status = status
				c.room.Detail = detail
				c.room.URL = url
			}
		}
		err := s.connector(ctx, r, update)
		s.mu.Lock()
		defer s.mu.Unlock()
		c.room.Status = "disconnected"
		c.room.Detail = ""
		c.room.URL = ""
		if err != nil && ctx.Err() == nil {
			c.room.Status = "error"
			c.room.Detail = client.SafeText(err.Error())
		}
		c.cancel = nil
	}()
	return nil
}
func (s *Server) disconnect(id string) {
	s.mu.Lock()
	c := s.active[id]
	if c != nil && c.cancel != nil {
		c.room.Status = "disconnecting"
		c.cancel()
	}
	s.mu.Unlock()
}
func (s *Server) list() ([]Room, []string) {
	rooms, warnings := s.catalog.List()
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for i, r := range rooms {
		seen[r.ID] = true
		if c := s.active[r.ID]; c != nil {
			rooms[i].Status = c.room.Status
			rooms[i].Detail = c.room.Detail
			rooms[i].URL = c.room.URL
		}
	}
	for id, c := range s.active {
		if !seen[id] {
			rooms = append(rooms, c.room)
		}
	}
	return rooms, warnings
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.listener.Addr().String() {
		http.Error(w, "invalid host", 403)
		return
	}
	origin := "http://" + r.Host
	if o := r.Header.Get("Origin"); o != "" && o != origin {
		http.Error(w, "invalid origin", 403)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if !strings.HasPrefix(r.URL.Path, s.path) {
		http.NotFound(w, r)
		return
	}
	route := strings.TrimPrefix(r.URL.Path, s.path)
	if r.Method == http.MethodGet {
		switch route {
		case "", "app.js", "style.css":
			file := route
			contentType := "text/html; charset=utf-8"
			if file == "" {
				file = "index.html"
			}
			if file == "app.js" {
				contentType = "text/javascript; charset=utf-8"
			}
			if file == "style.css" {
				contentType = "text/css; charset=utf-8"
			}
			data, _ := assets.ReadFile(file)
			w.Header().Set("Content-Type", contentType)
			w.Write(data)
		case "rooms":
			rooms, warnings := s.list()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(struct {
				Rooms    []Room   `json:"rooms"`
				Warnings []string `json:"warnings"`
				Version  string   `json:"version"`
			}{rooms, warnings, buildinfo.Version})
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Header.Get("Origin") != origin {
		http.Error(w, "same-origin request required", 403)
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		http.Error(w, "JSON required", 415)
		return
	}
	var body struct {
		ID      string `json:"id"`
		Address string `json:"address"`
		Session string `json:"session"`
		Name    string `json:"name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	var selected Room
	var err error
	switch route {
	case "add":
		selected, err = s.catalog.Add(body.Address, body.Session, body.Name)
	case "connect":
		rooms, _ := s.catalog.List()
		found := false
		for _, room := range rooms {
			if room.ID == body.ID {
				selected = room
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "room 不存在，请刷新列表", 404)
			return
		}
		if selected.Name == "" {
			selected, err = s.catalog.Add(selected.Address, selected.Session, body.Name)
		}
		if err == nil {
			err = s.connect(selected)
		}
	case "remove":
		s.mu.Lock()
		err = s.catalog.Remove(body.ID)
		if err == nil {
			if c := s.active[body.ID]; c != nil {
				if c.cancel != nil {
					c.cancel()
				}
				delete(s.active, body.ID)
			}
		}
		s.mu.Unlock()
	case "disconnect":
		s.disconnect(body.ID)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, client.SafeText(err.Error()), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		ID string `json:"id"`
	}{selected.ID})
}
