package network

import (
	"agent_romm/internal/room"
	"agent_romm/internal/workgroup"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
)

func (s *Server) EnableWorkers(b *workgroup.RemoteBroker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentWorkers = b
}
func (l Launcher) Worker(ctx context.Context, req workgroup.WorkerRequest) (workgroup.WorkerReply, error) {
	var out workgroup.WorkerReply
	c, hello, e := l.dialOperation(ctx, "agents")
	if e != nil {
		return out, e
	}
	defer c.Close()
	if hello.State != "approved" {
		return out, errors.New("Host 不支持本机 Agent 邀请或成员尚未获批，请更新 Host")
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	if e = writeWorkerJSON(c, req); e != nil {
		return out, e
	}
	if e = readWorkerJSON(c, &out); e != nil {
		return out, e
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}
func (s *Server) serveWorkers(c net.Conn, m room.Member, token string) {
	c.SetDeadline(time.Now().Add(60 * time.Second))
	var req workgroup.WorkerRequest
	if readWorkerJSON(c, &req) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	s.mu.Lock()

	var out workgroup.WorkerReply
	authenticated, e := s.store.AuthenticateJoin(ctx, s.room, token)
	if e == nil {
		var role string
		role, e = s.store.MemberRole(ctx, s.room, m.UID)
		if e == nil && role != "roommate" {
			e = errors.New("只有协作成员可以邀请和管理本机 Agent")
		}
	}
	if e == nil {
		if s.agentWorkers == nil {
			e = errors.New("Host 工作组不可用")
		} else {
			out, e = s.agentWorkers.HandleMember(authenticated, req)
		}
	}
	if e != nil {
		out = workgroup.WorkerReply{State: "error", Error: e.Error()}
	}
	s.mu.Unlock()
	writeWorkerJSON(c, out)
}

func readWorkerJSON(r io.Reader, out any) error {
	var prefix [4]byte
	if _, e := io.ReadFull(r, prefix[:]); e != nil {
		return e
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > 48<<20 {
		return errors.New("invalid worker frame")
	}
	data := make([]byte, n)
	if _, e := io.ReadFull(r, data); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing worker data")
	}
	return nil
}

func writeWorkerJSON(w io.Writer, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > 48<<20 {
		return errors.New("worker frame too large")
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	_, e = io.Copy(w, io.MultiReader(bytes.NewReader(n[:]), bytes.NewReader(b)))
	return e
}
