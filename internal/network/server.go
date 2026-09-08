package network

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agent_romm/internal/questions"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
)

type Sessions interface {
	ServeMember(context.Context, net.Conn, room.Member)
}
type Server struct {
	questions  *questions.Service
	privateDir string
	uploadMu   sync.Mutex
	store      *store.Store
	room       room.RoomID
	session    string
	listener   net.Listener
	admin      net.Listener
	http       *http.Server
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	conns      map[net.Conn]room.UID
	wg         sync.WaitGroup
	once       sync.Once
	errors     chan error
	adminPath  string
	adminInfo  os.FileInfo
}

func Start(ctx context.Context, address, privateDir string, rid room.RoomID, st *store.Store, sessions Sessions) (*Server, error) {
	cert, pin, err := Certificate(privateDir)
	if err != nil {
		return nil, err
	}
	if err = st.InitAdmission(ctx); err != nil {
		return nil, err
	}
	l, err := tls.Listen("tcp", address, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{privateDir: privateDir, store: st, room: rid, session: string(rid) + "." + pin, listener: l, ctx: ctx, cancel: cancel, conns: map[net.Conn]room.UID{}, errors: make(chan error, 2), adminPath: filepath.Join(privateDir, "network-admin.sock")}
	// Called only while the room's exclusive owner lock is held. A stale socket
	// from an earlier crashed process may be removed; regular files are refused.
	if info, e := os.Lstat(s.adminPath); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			l.Close()
			cancel()
			return nil, errors.New("admin endpoint is not a socket")
		}
		if e = os.Remove(s.adminPath); e != nil {
			l.Close()
			cancel()
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		l.Close()
		cancel()
		return nil, e
	}
	al, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.adminPath, Net: "unix"})
	if err != nil {
		l.Close()
		cancel()
		return nil, err
	}
	al.SetUnlinkOnClose(false)
	s.admin = al
	s.adminInfo, err = os.Lstat(s.adminPath)
	if err == nil {
		err = os.Chmod(s.adminPath, 0600)
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	var runner questions.SharedRunner
	if q, ok := sessions.(interface {
		Question(context.Context, room.Actor, room.ClientMessageID, string) (room.QuestionAnswer, error)
	}); ok {
		runner = q.Question
	}
	s.questions, err = questions.Open(privateDir, runner)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.http = &http.Server{Handler: http.HandlerFunc(s.manage), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 15 * time.Second}
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		err := s.http.Serve(al)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.errors <- err:
			default:
			}
		}
	}()
	go func() { defer s.wg.Done(); s.accept(sessions) }()
	return s, nil
}
func (s *Server) SessionID() string    { return s.session }
func (s *Server) Address() string      { return s.listener.Addr().String() }
func (s *Server) Errors() <-chan error { return s.errors }
func (s *Server) accept(sessions Sessions) {
	slots := make(chan struct{}, 64)
	for {
		c, err := s.listener.Accept()
		if err != nil {
			if s.ctx.Err() == nil {
				select {
				case s.errors <- err:
				default:
				}
			}
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			c.Close()
			<-slots
			return
		}
		s.conns[c] = 0
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer func() { c.Close(); s.mu.Lock(); delete(s.conns, c); s.mu.Unlock(); <-slots }()
			s.handle(c, sessions)
		}()
	}
}
func (s *Server) handle(c net.Conn, sessions Sessions) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	var h Hello
	if err := readJSON(c, &h); err != nil {
		return
	}
	if h.Operation != "" && h.Operation != "upload" && h.Operation != "download" && h.Operation != "questions" && h.Operation != "tasks" {
		writeJSON(c, Reply{State: "unsupported-operation"})
		return
	}
	if h.Session != s.session {
		_ = writeJSON(c, Reply{State: "invalid-session"})
		return
	}
	address, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	s.mu.Lock()
	r, err := s.store.RequestJoin(ctx, s.room, h.Token, h.Name, address)
	if err != nil {
		s.mu.Unlock()
		_ = writeJSON(c, Reply{State: "unavailable"})
		return
	}
	var member room.Member
	if r.State == "approved" {
		member, err = s.store.AuthenticateJoin(ctx, s.room, h.Token)
		if err == nil {
			s.conns[c] = member.UID
		}
	}
	s.mu.Unlock()
	if err != nil {
		_ = writeJSON(c, Reply{State: "unavailable"})
		return
	}
	if err = writeJSON(c, Reply{State: r.State, RequestID: r.ID}); err != nil || r.State != "approved" {
		return
	}
	_ = c.SetDeadline(time.Time{})
	if h.Operation == "tasks" {
		s.serveTasks(c, member, h.Token)
		return
	}
	if h.Operation == "questions" {
		s.serveQuestions(c, member, h.Token)
		return
	}
	if h.Operation == "download" {
		s.sendFile(c)
		return
	}
	if h.Operation == "upload" {
		role, err := s.store.MemberRole(s.ctx, s.room, member.UID)
		if err != nil || role != "roommate" {
			writeJSON(c, uploadReply{Error: "此身份不能上传附件或安排工作"})
			return
		}
		s.receiveUpload(c, h.Token)
		return
	}
	sessions.ServeMember(s.ctx, c, member)
}
func (s *Server) manage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/admission" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		rows, err := s.store.ListJoins(r.Context(), s.room)
		if err != nil {
			http.Error(w, "cannot list requests", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(rows)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var in struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		Role   string `json:"role"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid decision", 400)
		return
	}
	s.mu.Lock()
	result, err := s.store.DecideJoinRole(r.Context(), s.room, in.ID, in.Action, in.Role)
	if err == nil && (in.Action == "revoke" || in.Action == "role") {
		for c, uid := range s.conns {
			if uid == result.UID {
				c.Close()
			}
		}
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, "decision failed: request must be pending for approve/deny, approved for revoke, and member name must be unique", 409)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
func (s *Server) Close() error {
	s.once.Do(func() {
		s.cancel()
		if s.listener != nil {
			s.listener.Close()
		}
		if s.http != nil {
			s.http.Close()
		}
		if s.admin != nil {
			s.admin.Close()
		}
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		if s.questions != nil {
			s.questions.Close()
		}
		if info, e := os.Lstat(s.adminPath); e == nil && s.adminInfo != nil && os.SameFile(info, s.adminInfo) {
			os.Remove(s.adminPath)
		}
	})
	return nil
}

func Manage(ctx context.Context, privateDir, action, id string, out io.Writer) error {
	return ManageRole(ctx, privateDir, action, id, "", out)
}
func ManageRole(ctx context.Context, privateDir, action, id, role string, out io.Writer) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(privateDir, "network-admin.sock"))
	}}
	defer transport.CloseIdleConnections()
	method := http.MethodGet
	var body io.Reader
	if action != "requests" {
		method = http.MethodPost
		b, _ := json.Marshal(map[string]string{"action": action, "id": id, "role": role})
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix/admission", body)
	if err != nil {
		return err
	}
	res, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("host is not running for this state directory: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("%s", b)
	}
	_, err = out.Write(append(b, '\n'))
	return err
}
