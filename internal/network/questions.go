package network

import (
	"agent_romm/internal/questions"
	"agent_romm/internal/room"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
)

type QueryRequest struct {
	UID    room.UID `json:"uid,omitempty"`
	Action string   `json:"action"`
	ID     string   `json:"id,omitempty"`
	Text   string   `json:"text,omitempty"`
	Before int64    `json:"before,omitempty"`
}
type QueryReply struct {
	More    bool              `json:"more"`
	Role    string            `json:"role"`
	Ready   bool              `json:"ready"`
	Entries []questions.Entry `json:"entries,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func writeQuery(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > 2<<20 {
		return errors.New("question page too large")
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	_, err = io.Copy(w, io.MultiReader(bytes.NewReader(n[:]), bytes.NewReader(b)))
	return err
}
func (l Launcher) Query(ctx context.Context, request QueryRequest) (QueryReply, error) {
	c, r, err := l.dialOperation(ctx, "questions")
	if err != nil {
		return QueryReply{}, err
	}
	defer c.Close()
	if r.State != "approved" {
		return QueryReply{}, errors.New("membership not approved")
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(100 * time.Second))
	if err = writeJSON(c, request); err != nil {
		return QueryReply{}, err
	}
	var n [4]byte
	if _, err = io.ReadFull(c, n[:]); err != nil {
		return QueryReply{}, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > 2<<20 {
		return QueryReply{}, errors.New("question response too large")
	}
	b := make([]byte, size)
	if _, err = io.ReadFull(c, b); err != nil {
		return QueryReply{}, err
	}
	var reply QueryReply
	err = json.Unmarshal(b, &reply)
	if err == nil && reply.Error != "" {
		err = errors.New(reply.Error)
	}
	return reply, err
}
func (s *Server) serveQuestions(c net.Conn, m room.Member, token string) {
	c.SetDeadline(time.Now().Add(100 * time.Second))
	var req QueryRequest
	if readJSON(c, &req) != nil {
		return
	}
	role, err := s.store.MemberRole(s.ctx, s.room, m.UID)
	if err != nil {
		return
	}
	reply := QueryReply{Role: role, Ready: s.questions.Ready()}
	switch req.Action {
	case "info":
	case "list":
		if role == "visitor" {
			reply.Error = "参观者只能查看主聊天历史"
		} else {
			uid, all := m.UID, role == "roommate"
			if all && req.UID > 0 {
				uid = req.UID
				all = false
			}
			reply.Entries, err = s.questions.List(s.ctx, uid, all, req.Before)
		}
	case "ask":
		if role != "asker" {
			reply.Error = "仅询问者可在问答区提问"
		} else {
			ctx, cancel := context.WithTimeout(s.ctx, 95*time.Second)
			defer cancel()
			// Revocation closes this connection; stop generation when the peer closes too.
			go func() { var b [1]byte; c.Read(b[:]); cancel() }()
			var entry questions.Entry
			entry, err = s.questions.Ask(ctx, m, req.ID, req.Text)
			if err == nil {
				reply.Entries = []questions.Entry{entry}
			}
		}
	default:
		reply.Error = "invalid question operation"
	}
	if req.Action == "list" {
		reply.More = len(reply.Entries) == 50
		for len(reply.Entries) > 1 {
			b, _ := json.Marshal(reply)
			if len(b) < 1<<20 {
				break
			}
			reply.Entries = reply.Entries[:len(reply.Entries)-1]
			reply.More = true
		}
	}
	if err != nil {
		reply.Error = err.Error()
	}
	if _, err = s.store.AuthenticateJoin(s.ctx, s.room, token); err != nil {
		return
	}
	writeQuery(c, reply)
}
