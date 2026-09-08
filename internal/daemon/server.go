package daemon

import (
	"agent_romm/internal/identity"
	"agent_romm/internal/observability"
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	RoomID                                            room.RoomID
	RoomName, ProjectRoot, ExecutionOwner, SocketPath string
	SocketGID                                         uint32
	MaximumFrameBytes                                 uint32
	ClientBufferEvents, ClientBufferBytes             int
	RequestTimeout, HandshakeTimeout, IdleTimeout     time.Duration
}
type RoomService interface {
	Submit(context.Context, room.Actor, room.SubmitInput) (room.Acceptance, error)
	Note(context.Context, room.Actor, room.SubmitInput) (room.Acceptance, error)
	Steer(context.Context, room.Actor, room.SteerInput) (room.Acceptance, error)
	Cancel(context.Context, room.Actor, room.CancelInput) (room.Acceptance, error)
	Resolve(context.Context, room.Actor, room.RecoverInput) error
	Snapshot(context.Context) (room.Snapshot, error)
}
type Dependencies struct {
	Logger *observability.Logger
	Peers  identity.Resolver
	IDs    interface {
		NewConnectionID() (room.ConnectionID, error)
	}
	Members interface {
		FindMember(context.Context, room.RoomID, room.UID) (room.Member, error)
	}
	Events interface {
		LatestSeq(context.Context, room.RoomID) (room.Seq, error)
		Events(context.Context, room.RoomID, room.Seq, room.Seq, int) ([]room.DurableEvent, error)
	}
	Coordinator RoomService
	Hub         *Hub
	Connections interface {
		Connected(context.Context, room.ConnectionRecord) error
		Ack(context.Context, room.ConnectionID, room.Seq) error
		Disconnected(context.Context, room.ConnectionID, time.Time) error
		CloseStale(context.Context, room.RoomID, time.Time) error
	}
	ProjectView interface {
		Status(context.Context) ([]byte, error)
		Diff(context.Context) ([]byte, error)
	}
}
type RandomIDs struct{}

func (RandomIDs) NewConnectionID() (room.ConnectionID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return room.ConnectionID(hex.EncodeToString(b[:])), nil
}

var ErrConfiguration = errors.New("invalid daemon configuration")
var ErrSocketPath = errors.New("socket path is not available")

type Server struct {
	cfg      Config
	deps     Dependencies
	mu       sync.Mutex
	listener *net.UnixListener
	endpoint os.FileInfo
	conns    map[*net.UnixConn]struct{}
	wg       sync.WaitGroup
	stopped  bool
	started  bool
	cancel   context.CancelFunc
}

func NewServer(cfg Config, deps Dependencies) (*Server, error) {
	if cfg.RoomID == "" || cfg.RoomName == "" || cfg.ProjectRoot == "" || cfg.ExecutionOwner == "" || cfg.SocketPath == "" || deps.Members == nil || deps.Events == nil || deps.Coordinator == nil || deps.Connections == nil {
		return nil, ErrConfiguration
	}
	if cfg.MaximumFrameBytes == 0 {
		cfg.MaximumFrameBytes = protocol.MaxFrameBytes
	}
	if cfg.MaximumFrameBytes > protocol.MaxFrameBytes {
		return nil, ErrConfiguration
	}
	if cfg.ClientBufferEvents == 0 {
		cfg.ClientBufferEvents = 256
	}
	if cfg.ClientBufferBytes == 0 {
		cfg.ClientBufferBytes = 16 << 20
	}
	if cfg.ClientBufferEvents < 1 || cfg.ClientBufferEvents > 256 || cfg.ClientBufferBytes < 1 || cfg.ClientBufferBytes > 16<<20 {
		return nil, ErrConfiguration
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 90 * time.Second
	}
	if cfg.RequestTimeout < 0 || cfg.HandshakeTimeout < 0 || cfg.IdleTimeout < 0 {
		return nil, ErrConfiguration
	}
	if deps.Peers == nil {
		deps.Peers = identity.KernelResolver{}
	}
	if deps.IDs == nil {
		deps.IDs = RandomIDs{}
	}
	if deps.Hub == nil {
		deps.Hub = NewHub(cfg.ClientBufferEvents, cfg.ClientBufferBytes)
	}
	return &Server{cfg: cfg, deps: deps, conns: map[*net.UnixConn]struct{}{}}, nil
}
func (s *Server) Serve(parent context.Context) error {
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return ErrConfiguration
	}
	s.started = true
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	if _, err := os.Lstat(s.cfg.SocketPath); !errors.Is(err, os.ErrNotExist) {
		s.mu.Unlock()
		cancel()
		return ErrSocketPath
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.cfg.SocketPath, Net: "unix"})
	if err != nil {
		s.mu.Unlock()
		cancel()
		return ErrSocketPath
	}
	l.SetUnlinkOnClose(false)
	info, err := os.Lstat(s.cfg.SocketPath)
	if err == nil {
		s.endpoint = info
	}
	if err == nil {
		err = os.Chown(s.cfg.SocketPath, os.Geteuid(), int(s.cfg.SocketGID))
	}
	if err == nil {
		err = os.Chmod(s.cfg.SocketPath, 0660)
	}
	checked, statErr := os.Lstat(s.cfg.SocketPath)
	if err == nil && statErr == nil {
		st, ok := checked.Sys().(*syscall.Stat_t)
		if !ok || !os.SameFile(info, checked) || checked.Mode()&os.ModeSocket == 0 || checked.Mode().Perm() != 0660 || st.Gid != s.cfg.SocketGID || st.Uid != uint32(os.Geteuid()) {
			err = ErrSocketPath
		}
	} else {
		err = ErrSocketPath
	}
	if err != nil {
		l.Close()
		s.removeEndpoint()
		s.mu.Unlock()
		cancel()
		return ErrSocketPath
	}
	s.listener = l
	s.mu.Unlock()
	defer s.Shutdown(context.Background())
	checkCtx, checkCancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	err = s.deps.Connections.CloseStale(checkCtx, s.cfg.RoomID, time.Now())
	checkCancel()
	if err != nil {
		return errors.New("connection initialization failed")
	}
	go func() { <-ctx.Done(); s.Shutdown(context.Background()) }()
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return nil
			}
			return errors.New("socket accept failed")
		}
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			c.Close()
			return nil
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer func() { c.Close(); s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
			s.session(ctx, c)
		}()
	}
}

// removeEndpoint is called with mu held; never unlink a replacement endpoint.
func (s *Server) removeEndpoint() {
	if s.endpoint != nil {
		if info, err := os.Lstat(s.cfg.SocketPath); err == nil && os.SameFile(s.endpoint, info) {
			_ = os.Remove(s.cfg.SocketPath)
		}
	}
}
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.listener != nil {
		s.listener.Close()
	}
	for c := range s.conns {
		c.Close()
	}
	s.removeEndpoint()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Question is reachable only through the authenticated ask-role network path.
func (s *Server) Question(ctx context.Context, actor room.Actor, id room.ClientMessageID, text string) (room.QuestionAnswer, error) {
	q, ok := s.deps.Coordinator.(interface {
		Question(context.Context, room.Actor, room.ClientMessageID, string) (room.QuestionAnswer, error)
	})
	if !ok {
		return room.QuestionAnswer{}, errors.New("Host 不支持只读问答")
	}
	return q.Question(ctx, actor, id, text)
}
