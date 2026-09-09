package network

import (
	"agent_romm/internal/personal"
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

func (s *Server) EnablePersonal(b *personal.Broker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.personal = b
	if b != nil {
		b.SetMode(s.resourceMode)
	}
}
func (s *Server) servePersonalResource(c net.Conn, m room.Member, req ResourceRequest) {
	s.mu.Lock()
	b, mode := s.personal, s.resourceMode
	s.mu.Unlock()
	role, err := s.store.MemberRole(s.ctx, s.room, m.UID)
	reply := ResourceReply{Type: "done", Mode: mode, Role: role}
	if b == nil || mode != "personal" || err != nil || role != "roommate" {
		reply.Error = "个人创建不可用或没有协作权限"
	} else if req.Action == "personal-pending" {
		reply.Personal, err = b.Next(s.ctx, m.UID, req.ClientID)
	} else if req.PersonalResult == nil {
		err = errors.New("missing result")
	} else {
		err = b.Resolve(s.ctx, m.UID, req.ClientID, *req.PersonalResult)
	}
	if err != nil {
		reply.Error = "个人创建请求已过期或不属于当前成员"
	}
	writeQuery(c, reply)
}
func (s *Server) servePersonalCreate(out http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		out.WriteHeader(405)
		return
	}
	var in struct {
		Capability string `json:"capability"`
		Title      string `json:"title"`
		Content    string `json:"content"`
	}
	d := json.NewDecoder(http.MaxBytesReader(out, r.Body, 196608))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(out, "invalid request", 400)
		return
	}
	s.mu.Lock()
	b := s.personal
	s.mu.Unlock()
	if b == nil {
		http.Error(out, "个人创建未启用；禁止回退 Host", 503)
		return
	}
	http.NewResponseController(out).SetWriteDeadline(time.Now().Add(6 * time.Minute))
	result, err := b.Call(r.Context(), in.Capability, in.Title, in.Content)
	if err != nil {
		http.Error(out, err.Error(), 409)
		return
	}
	out.Header().Set("Content-Type", "application/json")
	json.NewEncoder(out).Encode(result)
}
func PersonalCreate(ctx context.Context, privateDir string, body io.Reader, out io.Writer) error {
	return personalOperation(ctx, privateDir, "personal-create", body, out)
}
func PersonalCommit(ctx context.Context, privateDir string, body io.Reader, out io.Writer) error {
	return personalOperation(ctx, privateDir, "personal-commit", body, out)
}
func personalOperation(ctx context.Context, privateDir, operation string, body io.Reader, out io.Writer) error {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(privateDir, "network-admin.sock"))
	}}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/"+operation, body)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return errors.New("个人创建连接未完成；只能检查原请求或询问发送者，禁止使用 Host 代建")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 48001))
	if err != nil || len(data) > 48000 {
		return errors.New("创建响应无效，请在发送者本机查询回执")
	}
	if resp.StatusCode != 200 {
		return errors.New(string(data))
	}
	_, err = out.Write(data)
	return err
}

func (s *Server) servePersonalCommit(out http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		out.WriteHeader(405)
		return
	}
	var in struct {
		Capability string               `json:"capability"`
		Commit     personal.CommitInput `json:"commit"`
	}
	d := json.NewDecoder(http.MaxBytesReader(out, r.Body, 65536))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(out, "invalid request", 400)
		return
	}
	s.mu.Lock()
	b := s.personal
	s.mu.Unlock()
	if b == nil {
		http.Error(out, "个人提交不可用", 503)
		return
	}
	root, err := s.store.ProjectRoot(r.Context(), s.room)
	if err != nil {
		http.Error(out, "项目不可用", 503)
		return
	}
	http.NewResponseController(out).SetWriteDeadline(time.Now().Add(6 * time.Minute))
	result, err := b.Commit(r.Context(), in.Capability, root, filepath.Join(s.privateDir, "git-commit-receipts"), in.Commit)
	if err != nil {
		http.Error(out, err.Error(), 409)
		return
	}
	out.Header().Set("Content-Type", "application/json")
	json.NewEncoder(out).Encode(result)
}
