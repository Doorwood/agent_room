package network

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent_romm/internal/personal"
	"agent_romm/internal/resources"
	"agent_romm/internal/room"
)

type ResourceRequest struct {
	ClientID       string            `json:"clientId,omitempty"`
	PersonalResult *personal.Result  `json:"personalResult,omitempty"`
	Action         string            `json:"action"`
	Mode           string            `json:"mode,omitempty"`
	Resource       resources.Request `json:"resource,omitempty"`
	Question       string            `json:"question,omitempty"`
}
type ResourceReply struct {
	Personal     *personal.Request `json:"personal,omitempty"`
	SummaryError string            `json:"summaryError,omitempty"`
	Type         string            `json:"type"`
	Mode         string            `json:"mode,omitempty"`
	Role         string            `json:"role,omitempty"`
	Resource     resources.Request `json:"resource,omitempty"`
	Text         string            `json:"text,omitempty"`
	Answer       string            `json:"answer,omitempty"`
	Error        string            `json:"error,omitempty"`
}
type ResourceSummary func(context.Context, string, string) (string, error)

func readResource(r io.Reader, v any) error {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size == 0 || size > 384000 {
		return errors.New("resource frame too large")
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
func (l Launcher) Resources(ctx context.Context, req ResourceRequest, execute resources.Execute) (ResourceReply, error) {
	c, hello, err := l.dialOperation(ctx, "resources")
	if err != nil {
		return ResourceReply{}, err
	}
	defer c.Close()
	if hello.State != "approved" {
		return ResourceReply{}, errors.New("Host 不支持资源授权或成员未批准，请更新 Host")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(95 * time.Second))
	if err = writeQuery(c, req); err != nil {
		return ResourceReply{}, err
	}
	type incoming struct {
		reply ResourceReply
		err   error
	}
	received := make(chan incoming, 1)
	go func() {
		for {
			var reply ResourceReply
			err := readResource(c, &reply)
			select {
			case received <- incoming{reply, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				cancel()
				return
			}
			if reply.Type != "execute" {
				return
			}
		}
	}()
	called := false
	localWriteResult := ""
	for {
		var msg incoming
		select {
		case msg = <-received:
		case <-ctx.Done():
			return ResourceReply{}, ctx.Err()
		}
		reply := msg.reply
		if msg.err != nil {
			return reply, msg.err
		}
		if reply.Error != "" {
			return reply, errors.New(reply.Error)
		}
		if reply.Type == "execute" {
			if called || req.Action != "query" || req.Mode != "personal" || reply.Mode != "personal" || reply.Resource != req.Resource || execute == nil {
				return ResourceReply{}, errors.New("Host 请求超出本次本机授权")
			}
			called = true
			text, err := execute(ctx, req.Resource)
			if req.Resource.IsWrite() && err == nil {
				localWriteResult = text
			}
			response := ResourceReply{Type: "result", Text: text}
			if err != nil {
				response.Text = ""
				response.Error = "个人客户端拒绝或无法完成资源调用，未回退 Host"
			}
			if len(text) > resources.MaxResult {
				return ResourceReply{}, errors.New("resource too large")
			}
			if err = writeQuery(c, response); err != nil {
				return ResourceReply{}, err
			}
			continue
		}
		if reply.Type != "done" && reply.Type != "info" {
			return reply, errors.New("invalid resource response")
		}
		if req.Resource.IsWrite() {
			if !called || localWriteResult == "" {
				return ResourceReply{}, errors.New("没有本机创建回执，不能确认文档已创建")
			}
			reply.Text = localWriteResult
			reply.Answer = ""
		}
		return reply, nil
	}
}
func (s *Server) EnableResources(execute resources.Execute, summary ResourceSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resourceExecute = execute
	s.resourceSummary = summary
}
func (s *Server) serveResources(c net.Conn, m room.Member, token string) {
	c.SetDeadline(time.Now().Add(95 * time.Second))
	var req ResourceRequest
	if readResource(c, &req) != nil {
		return
	}
	if req.Action == "personal-pending" || req.Action == "personal-result" {
		s.servePersonalResource(c, m, req)
		return
	}
	s.mu.Lock()
	mode := s.resourceMode
	role, err := s.store.MemberRole(s.ctx, s.room, m.UID)
	execute, summary := s.resourceExecute, s.resourceSummary
	if req.Action == "info" {
		s.mu.Unlock()
		writeQuery(c, ResourceReply{Type: "info", Mode: mode, Role: role})
		return
	}
	if err != nil || (role != "roommate" && role != "asker") || req.Action != "query" || req.Mode != mode || req.Resource.Validate() != nil || len(req.Question) > 6000 || (role == "asker" && mode == "host") || (req.Resource.IsWrite() && (role != "roommate" || mode != "personal" || req.Question != "")) {
		s.mu.Unlock()
		writeQuery(c, ResourceReply{Type: "done", Error: "资源授权模式已变化、角色不允许或请求无效，请刷新；不会回退其他身份"})
		return
	}
	if s.resourceBusy[m.UID] || len(s.resourceBusy) >= 4 {
		s.mu.Unlock()
		writeQuery(c, ResourceReply{Type: "done", Error: "资源请求正在处理，请稍后重试"})
		return
	}
	s.resourceBusy[m.UID] = true
	s.resourceConns[c] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.resourceBusy, m.UID); delete(s.resourceConns, c); s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
	defer cancel()
	var text string
	if mode == "personal" {
		err = writeQuery(c, ResourceReply{Type: "execute", Mode: mode, Resource: req.Resource})
		if err == nil {
			var result ResourceReply
			err = readResource(c, &result)
			if err == nil {
				if result.Type != "result" || result.Error != "" || len(result.Text) > resources.MaxResult {
					err = errors.New("个人客户端未完成调用")
				} else {
					text = result.Text
				}
			}
		}
	}
	// After the one client result, any byte or disconnect cancels this request.
	go func() { var b [1]byte; c.Read(b[:]); cancel() }()
	if mode == "host" {
		if execute == nil {
			err = errors.New("Host 未启用资源工具")
		} else {
			text, err = execute(ctx, req.Resource)
		}
	}
	reply := ResourceReply{Type: "done", Mode: mode, Role: role}
	if err == nil {
		reply.Text = text
		if strings.TrimSpace(req.Question) != "" {
			if summary == nil {
				reply.SummaryError = "Host 不支持隔离资源问答；原始资源已读取"
			} else {
				reply.Answer, err = summary(ctx, req.Question, text)
				if err != nil {
					reply.Answer = ""
					reply.SummaryError = "隔离模型问答未完成，原始资源已读取；未使用主会话。Host 需要支持临时会话且有可复用的 Codex 文件登录。"
					err = nil
				}
			}
		}
	}
	if err != nil {
		reply.Error = "资源调用或隔离问答失败，请检查当前授权与连接；未切换身份或写入主会话"
	}
	s.mu.Lock()
	_, authErr := s.store.AuthenticateJoin(s.ctx, s.room, token)
	sameMode := s.resourceMode == mode
	s.mu.Unlock()
	if authErr != nil || !sameMode || ctx.Err() != nil {
		return
	}
	writeQuery(c, reply)
}
func (s *Server) manageResourceMode(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		s.mu.Lock()
		mode := s.resourceMode
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"mode": mode})
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || (in.Mode != "host" && in.Mode != "personal") {
		http.Error(w, "mode must be host or personal", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	persist := func() error {
		if s.personal != nil && in.Mode != s.resourceMode {
			active, err := s.store.HasActiveWork(r.Context(), s.room)
			if err != nil || active {
				return errors.New("请先停止或等待当前工作结束，再切换资源身份，避免同一轮混用账号")
			}
		}
		path := filepath.Join(s.privateDir, "resource-mode")
		f, err := os.CreateTemp(s.privateDir, ".resource-mode-")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, err = f.WriteString(in.Mode)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(f.Name(), path)
		}
		return err
	}
	var err error
	if s.personal != nil {
		err = s.personal.ChangeMode(in.Mode, persist)
	} else {
		err = persist()
	}
	if err != nil {
		http.Error(w, "无法切换资源身份：请先停止或等待当前任务结束，并检查配置目录是否可写", 409)
		return
	}
	s.resourceMode = in.Mode
	for c := range s.resourceConns {
		c.Close()
	}
	json.NewEncoder(w).Encode(map[string]string{"mode": in.Mode})
}
func ResourceMode(ctx context.Context, privateDir, mode string, out io.Writer) error {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(privateDir, "network-admin.sock"))
	}}
	defer tr.CloseIdleConnections()
	method := "GET"
	var body io.Reader
	if mode != "" {
		method = "POST"
		b, _ := json.Marshal(map[string]string{"mode": mode})
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix/resource-mode", body)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("无法设置资源授权方式")
	}
	_, err = io.Copy(out, io.LimitReader(resp.Body, 1024))
	return err
}
