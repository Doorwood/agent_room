package daemon

import (
	"agent_romm/internal/observability"
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"
)

type WireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Resumable bool   `json:"resumable,omitempty"`
}
type sessionWriter struct {
	conn    net.Conn
	writer  *protocol.Writer
	timeout time.Duration
}

// MaximumMutationBodyBytes is deliberately below the transport frame limit.
// It bounds all client-controlled mutation fields together, including recovery
// instructions and control turn IDs. Even sixfold JSON escaping and duplicated
// acceptance/event payloads leave over 7 MiB for server metadata in an 8 MiB frame.
const MaximumMutationBodyBytes = 64 << 10

var errResponseEncoding = errors.New("response cannot be encoded within frame budget")
var errFrameWrite = errors.New("frame transport write failed")

func (w sessionWriter) send(kind protocol.Kind, id, method string, body any, seq *uint64) error {
	data, err := json.Marshal(body)
	if err != nil {
		return errResponseEncoding
	}
	envelope := protocol.Envelope{Version: 1, Kind: kind, ID: id, Method: method, Body: data, Seq: seq}
	if err := envelope.Validate(); err != nil {
		return errResponseEncoding
	}
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) > int(protocol.MaxFrameBytes) {
		return errResponseEncoding
	}
	_ = w.conn.SetWriteDeadline(time.Now().Add(w.timeout))
	if err := w.writer.Write(envelope); err != nil {
		return errFrameWrite
	}
	return nil
}

// A fallback is safe only after preflight failure, before any frame bytes have
// been written. Transport errors may mean a partial frame and must close the peer.
func (w sessionWriter) response(id, method string, body any) error {
	err := w.send(protocol.KindResponse, id, method, body, nil)
	if errors.Is(err, errResponseEncoding) {
		return w.fail(id, "response-too-large")
	}
	return err
}
func (w sessionWriter) fail(id, code string) error {
	return w.send(protocol.KindError, id, "error", WireError{Code: code, Message: "Request could not be completed.", Resumable: code == "slow-client"}, nil)
}

type readResult struct {
	env protocol.Envelope
	err error
}

func (s *Server) session(ctx context.Context, c *net.UnixConn) {
	peer, err := s.deps.Peers.Resolve(c)
	if err != nil {
		return
	}
	authCtx, cancel := context.WithTimeout(ctx, s.cfg.HandshakeTimeout)
	member, err := s.deps.Members.FindMember(authCtx, s.cfg.RoomID, peer.UID)
	cancel()
	if err != nil || member.UID != peer.UID {
		_ = (sessionWriter{c, protocol.NewWriter(c), s.cfg.RequestTimeout}).fail("", "member-not-found")
		return
	}
	s.ServeMember(ctx, c, member)
}

// ServeMember serves an identity already authenticated by an embedding transport.
// The caller owns connection registration, revocation and shutdown. No identity
// field received through the room protocol can override this member.
func (s *Server) ServeMember(ctx context.Context, c net.Conn, member room.Member) {
	defer c.Close()
	w := sessionWriter{c, protocol.NewWriter(c), s.cfg.RequestTimeout}
	r := protocol.NewReader(c, s.cfg.MaximumFrameBytes)
	authCtx, authCancel := context.WithTimeout(ctx, s.cfg.HandshakeTimeout)
	stored, err := s.deps.Members.FindMember(authCtx, s.cfg.RoomID, member.UID)
	authCancel()
	if err != nil || stored != member {
		return
	}
	actor := room.Actor{UID: member.UID, Name: member.Name}
	_ = c.SetReadDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	helloEnv, err := r.Read()
	if err != nil {
		_ = w.fail("", "invalid-request")
		return
	}
	if err = protocol.NewHandshakeState(nil).Accept(helloEnv); err != nil {
		_ = w.fail(helloEnv.ID, "invalid-request")
		return
	}
	hello, _ := protocol.DecodeBody[protocol.Hello](helloEnv.Body)
	id, err := s.deps.IDs.NewConnectionID()
	if err != nil || !room.ValidConnectionID(id) {
		_ = w.fail(helloEnv.ID, "internal-error")
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	err = s.deps.Connections.Connected(opCtx, room.ConnectionRecord{ID: id, RoomID: s.cfg.RoomID, UID: actor.UID, ConnectedAt: time.Now()})
	cancel()
	if err != nil {
		_ = w.fail(helloEnv.ID, "internal-error")
		return
	}
	defer func() {
		dc, done := context.WithTimeout(context.Background(), s.cfg.RequestTimeout)
		defer done()
		_ = s.deps.Connections.Disconnected(dc, id, time.Now())
	}()
	sub := s.deps.Hub.Subscribe(actor).(*subscription)
	defer sub.Close()
	revision, err := s.replay(ctx, w, helloEnv.ID, room.Seq(hello.LastAppliedSeq), sub)
	if err != nil {
		if errors.Is(err, errFrameWrite) {
			return
		}
		code := "replay-failed"
		if sub.Closed() {
			code = "slow-client"
		}
		_ = w.fail("", code)
		return
	}
	incoming := make(chan readResult, 1)
	readerCtx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
	go func() {
		for {
			env, err := r.Read()
			select {
			case incoming <- readResult{env, err}:
			case <-readerCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	idle := time.NewTimer(s.cfg.IdleTimeout)
	defer idle.Stop()
	_ = c.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			return
		case <-sub.done:
			_ = w.fail("", "slow-client")
			return
		case b, ok := <-sub.events:
			if !ok {
				return
			}
			if b.Transient != nil && b.Transient.Revision <= revision {
				continue
			}
			if err := sendBroadcast(w, b); err != nil {
				return
			}
		case rr := <-incoming:
			if rr.err != nil {
				_ = w.fail("", "invalid-request")
				return
			}
			requestCtx, done := context.WithTimeout(ctx, s.cfg.RequestTimeout)
			valid, writeErr := s.dispatch(requestCtx, w, actor, id, rr.env)
			done()
			if writeErr != nil {
				return
			}
			if valid {
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(s.cfg.IdleTimeout)
				_ = c.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
			}
		}
	}
}
func (s *Server) dispatch(ctx context.Context, w sessionWriter, actor room.Actor, id room.ConnectionID, e protocol.Envelope) (bool, error) {
	invalid := func() (bool, error) { return false, w.fail(e.ID, "invalid-request") }
	if e.Kind != protocol.KindRequest || len(e.Requires) > 0 {
		return invalid()
	}
	if roles, ok := s.deps.Members.(interface {
		MemberRole(context.Context, room.RoomID, room.UID) (string, error)
	}); ok {
		role, err := roles.MemberRole(ctx, s.cfg.RoomID, actor.UID)
		if err != nil {
			return false, w.fail(e.ID, "permission-denied")
		}
		if role != "roommate" {
			switch e.Method {
			case "ack", "heartbeat", "who", "members":
			default:
				return false, w.fail(e.ID, "read-only-role")
			}
		}
	}
	switch e.Method {
	case "submit", "task_submit", "note", "steer", "cancel", "recover", "resolve":
		if len(e.Body) > MaximumMutationBodyBytes {
			return false, w.fail(e.ID, "message-too-large")
		}
	}
	var out any = protocol.Empty{}
	var err error
	switch e.Method {
	case "submit", "task_submit", "note":
		in, decodeErr := protocol.DecodeBody[protocol.SubmitRequest](e.Body)
		if decodeErr != nil {
			return invalid()
		}
		if (e.Method == "task_submit") != (in.TaskID > 0) {
			return invalid()
		}
		input := room.SubmitInput{ClientMessageID: room.ClientMessageID(in.ClientMessageID), Text: in.Text, TaskID: in.TaskID}
		if e.Method != "note" {
			out, err = s.deps.Coordinator.Submit(ctx, actor, input)
		} else {
			out, err = s.deps.Coordinator.Note(ctx, actor, input)
		}
	case "steer":
		in, de := protocol.DecodeBody[protocol.SteerRequest](e.Body)
		if de != nil {
			return invalid()
		}
		out, err = s.deps.Coordinator.Steer(ctx, actor, room.SteerInput{ClientMessageID: room.ClientMessageID(in.ClientMessageID), ExpectedTurnID: room.TurnID(in.ExpectedTurnID), Text: in.Text})
	case "cancel":
		in, de := protocol.DecodeBody[protocol.CancelRequest](e.Body)
		if de != nil {
			return invalid()
		}
		out, err = s.deps.Coordinator.Cancel(ctx, actor, room.CancelInput{ClientMessageID: room.ClientMessageID(in.ClientMessageID), ExpectedTurnID: room.TurnID(in.ExpectedTurnID)})
	case "recover", "resolve":
		in, de := protocol.DecodeBody[protocol.RecoverRequest](e.Body)
		if de != nil {
			return invalid()
		}
		err = s.deps.Coordinator.Resolve(ctx, actor, room.RecoverInput{ClientMessageID: room.ClientMessageID(in.ClientMessageID), TargetMessageID: room.ClientMessageID(in.TargetMessageID), Action: room.RecoveryAction(in.Action), ReplacementMessageID: room.ClientMessageID(in.ReplacementMessageID), Instruction: in.Instruction})
	case "ack":
		in, de := protocol.DecodeBody[protocol.Ack](e.Body)
		if de != nil {
			return invalid()
		}
		err = s.deps.Connections.Ack(ctx, id, room.Seq(in.Seq))
	case "heartbeat":
		in, de := protocol.DecodeBody[protocol.Heartbeat](e.Body)
		if de != nil {
			return invalid()
		}
		out = in
	case "queue", "status", "who", "members", "diff":
		if _, de := protocol.DecodeBody[protocol.Empty](e.Body); de != nil {
			return invalid()
		}
		switch e.Method {
		case "queue", "status":
			out, err = s.deps.Coordinator.Snapshot(ctx)
		case "who":
			out = s.deps.Hub.Members()
		case "members":
			if directory, ok := s.deps.Members.(interface {
				ListMembers(context.Context, room.RoomID) ([]room.Member, error)
			}); ok {
				out, err = directory.ListMembers(ctx, s.cfg.RoomID)
			} else {
				out = s.deps.Hub.Members()
			}
		case "diff":
			if s.deps.ProjectView == nil {
				err = ErrConfiguration
			} else {
				var data []byte
				data, err = s.deps.ProjectView.Diff(ctx)
				out = struct {
					Diff string `json:"diff"`
				}{string(data)}
			}
		}
	default:
		return invalid()
	}
	if accepted, ok := out.(room.Acceptance); ok && accepted.Seq > 0 && !accepted.Duplicate && s.deps.Logger != nil {
		s.deps.Logger.AcceptedMessage(ctx, observability.MessageFields{ConnectionID: id, ActorUID: actor.UID, ClientMessageID: accepted.ClientMessageID, Seq: accepted.Seq, Bytes: uint64(len(e.Body))})
	}
	if err != nil {
		code := "request-failed"
		if errors.Is(err, room.ErrStaleTurn) {
			code = "stale-turn"
		}
		if errors.Is(err, room.ErrStaleRecovery) {
			code = "stale-recovery"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = "request-timeout"
		}
		return true, w.fail(e.ID, code)
	}
	return true, w.response(e.ID, e.Method, out)
}
