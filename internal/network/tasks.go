package network

import (
	"agent_romm/internal/room"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
)

// Tasks uses a separate operation so an older host rejects unsupported writes.
func (l Launcher) Tasks(ctx context.Context, req room.TaskRequest) (room.TaskReply, error) {
	var reply room.TaskReply
	c, hello, err := l.dialOperation(ctx, "tasks")
	if err != nil {
		return reply, err
	}
	defer c.Close()
	if hello.State != "approved" {
		return reply, errors.New("Host 不支持项目任务或身份尚未批准，请更新 Host")
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	if err = writeJSON(c, req); err != nil {
		return reply, err
	}
	var size [4]byte
	if _, err = io.ReadFull(c, size[:]); err != nil {
		return reply, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n > 2<<20 {
		return reply, errors.New("task response too large")
	}
	b := make([]byte, n)
	if _, err = io.ReadFull(c, b); err != nil {
		return reply, err
	}
	err = json.Unmarshal(b, &reply)
	if err == nil && reply.Error != "" {
		err = errors.New(reply.Error)
	}
	return reply, err
}
func (s *Server) serveTasks(c net.Conn, m room.Member, token string) {
	c.SetDeadline(time.Now().Add(15 * time.Second))
	var req room.TaskRequest
	if readJSON(c, &req) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	var reply room.TaskReply
	var err error
	// Serialize membership validation and writes with revocation/role changes.
	s.mu.Lock()
	_, err = s.store.AuthenticateJoin(ctx, s.room, token)
	if err == nil {
		if req.Action == "list" || req.Action == "get" {
			reply, err = s.store.Tasks(ctx, s.room, req)
		} else {
			var task room.ProjectTask
			task, err = s.store.ChangeTask(ctx, s.room, room.Actor{UID: m.UID, Name: m.Name}, req)
			reply.Task = &task
		}
	}
	s.mu.Unlock()
	if err != nil {
		reply = room.TaskReply{Error: err.Error()}
	}
	// JSON escaping can expand text substantially. Preserve pagination rather
	// than failing an otherwise valid page or dropping its newest run.
	if reply.Task != nil {
		for len(reply.Task.Runs) > 1 {
			b, _ := json.Marshal(reply)
			if len(b) <= 2<<20 {
				break
			}
			reply.Task.Runs = reply.Task.Runs[:len(reply.Task.Runs)-1]
			reply.More = true
		}
	}
	writeQuery(c, reply)
}
