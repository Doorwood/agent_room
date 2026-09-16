package dashboard

import (
	"agent_romm/internal/network"
	"agent_romm/internal/workgroup"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type localAgent struct {
	AgentName      string    `json:"agentName"`
	Name           string    `json:"name"`
	WorkspaceScope string    `json:"workspaceScope"`
	ID             string    `json:"id"`
	RoomID         string    `json:"roomId"`
	Provider       string    `json:"provider"`
	Project        string    `json:"project"`
	Mode           string    `json:"mode"`
	WorkerID       string    `json:"workerId"`
	State          string    `json:"state"`
	Detail         string    `json:"detail,omitempty"`
	LastHealthy    time.Time `json:"lastHealthy,omitempty"`
	CheckMillis    int64     `json:"checkMillis,omitempty"`
	recheck        chan struct{}
	cancel         context.CancelFunc
}
type inviteBody struct {
	AgentName      string `json:"agentName"`
	WorkspaceScope string `json:"workspaceScope"`
	ID             string `json:"id"`
	Invitation     string `json:"invitation"`
	Provider       string `json:"provider"`
	Project        string `json:"project"`
	Mode           string `json:"mode"`
}

func (s *Server) agentList(w http.ResponseWriter) {
	s.mu.Lock()
	var running []localAgent
	for _, a := range s.localAgents {
		running = append(running, *a)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Installed []workgroup.Installed `json:"installed"`
		Running   []localAgent          `json:"running"`
	}{workgroup.Detect(), running})
}
func (s *Server) inviteAgent(ctx context.Context, body inviteBody) (*localAgent, error) {
	if body.Provider != "cursor" && body.Provider != "claude-code" && body.Provider != "codex" {
		return nil, fmt.Errorf("请选择 Codex、Cursor 或 Claude Code")
	}
	name, err := workgroup.AgentName(body.Provider, body.AgentName)
	if err != nil {
		return nil, err
	}
	body.AgentName = name
	if body.Mode != "review" && body.Mode != "work" {
		return nil, fmt.Errorf("请选择只读或编辑权限")
	}
	installed := false
	for _, a := range workgroup.Detect() {
		if a.Provider == body.Provider && a.Installed {
			installed = true
		}
	}
	if !installed {
		return nil, fmt.Errorf("本机未检测到此 Agent CLI")
	}
	scope := body.WorkspaceScope
	if scope == "" {
		if body.Project != "" {
			scope = "local"
		} else {
			scope = "host"
		}
	}
	if scope != "host" && scope != "local" {
		return nil, fmt.Errorf("请选择 Host 隔离副本或本机项目")
	}
	root := ""
	var e error
	if scope == "local" {
		if !filepath.IsAbs(body.Project) {
			return nil, fmt.Errorf("请填写本机项目绝对路径")
		}
		root, e = filepath.EvalSymlinks(body.Project)
		if e != nil {
			return nil, e
		}
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() {
			return nil, fmt.Errorf("本机项目目录不存在")
		}
	}
	if body.Invitation == "" {
		var token [16]byte
		if _, e = rand.Read(token[:]); e != nil {
			return nil, e
		}
		body.Invitation = hex.EncodeToString(token[:])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("Dashboard 已关闭")
	}
	c := s.active[body.ID]
	if c == nil || c.room.Status != "connected" || c.ctx == nil {
		return nil, fmt.Errorf("请先以已批准的协作成员身份连接 Room")
	}
	key := body.ID + ":" + body.Invitation
	if existing := s.localAgents[key]; existing != nil {
		if existing.State == "offline" || existing.State == "error" {
			return nil, fmt.Errorf("本次邀请已结束，请关闭窗口后重新邀请")
		}
		if existing.AgentName != body.AgentName || existing.WorkspaceScope != scope || existing.Provider != body.Provider || existing.Project != root || existing.Mode != body.Mode {
			return nil, fmt.Errorf("邀请参数冲突")
		}
		copy := *existing
		return &copy, nil
	}
	if len(s.localAgents) >= 32 {
		return nil, fmt.Errorf("请先移除已有 Agent")
	}
	credential, e := network.CredentialFor(s.catalog.credentialDir(), c.room.Address, c.room.Session, c.room.Name)
	if e != nil {
		return nil, e
	}
	launcher := network.Launcher{Credential: credential}
	// Holding the local management lock prevents disconnect from racing the
	// ownership check; the remote operation has its own bounded deadline.
	reply, e := launcher.Worker(ctx, workgroup.WorkerRequest{Action: "invite", AgentName: body.AgentName, ID: body.Invitation, Provider: body.Provider, Mode: body.Mode, WorkspaceScope: scope})
	if e != nil {
		return nil, e
	}
	life, cancel := context.WithCancel(c.ctx)
	a := &localAgent{AgentName: body.AgentName, Name: reply.Name, WorkspaceScope: scope, ID: key, RoomID: body.ID, Provider: body.Provider, Project: root, Mode: body.Mode, WorkerID: reply.ID, State: "checking", recheck: make(chan struct{}, 1), cancel: cancel}
	s.localAgents[key] = a
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.runLocalAgent(life, launcher, a) }()
	copy := *a
	return &copy, nil
}
func (s *Server) agentStatus(a *localAgent, state, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.State = state
	a.Detail = detail
}
func (s *Server) runLocalAgent(ctx context.Context, l network.Launcher, a *localAgent) {
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = l.Worker(stop, workgroup.WorkerRequest{Action: "leave", ID: a.WorkerID})
		s.mu.Lock()
		a.State = "offline"
		s.mu.Unlock()
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	seen := map[string]bool{}
	jobID := ""
	var stopJob context.CancelFunc
	type outcome struct {
		result workgroup.Result
		err    error
	}
	var results chan outcome
	defer func() {
		if stopJob != nil {
			stopJob()
			if results != nil {
				<-results
			}
		}
	}()
	needsCheck := true
	lastHealthy := time.Time{}
	for {
		select {
		case <-a.recheck:
			needsCheck = true
		default:
		}
		if jobID == "" && (needsCheck || time.Since(lastHealthy) > 30*time.Minute) {
			accepted, err := l.Worker(ctx, workgroup.WorkerRequest{Action: "health", ID: a.WorkerID, Healthy: false})
			if err == nil && accepted.State != "busy" {
				if accepted.State != "health-accepted" {
					s.agentStatus(a, "error", "Host 不支持健康检查，请更新 Host")
					return
				}
				if !s.checkAgent(ctx, l, a) {
					return
				}
				needsCheck = false
				lastHealthy = time.Now()
			} else if err != nil {
				s.agentStatus(a, "error", "Host 健康检查协议或连接失败，请更新 Host 并重新邀请："+err.Error())
				return
			}
		}
		reply, e := l.Worker(ctx, workgroup.WorkerRequest{Action: "poll", ID: a.WorkerID})
		if e != nil {
			if ctx.Err() == nil {
				s.agentStatus(a, "error", "连接中断，本次工作结果需核对；不会自动重跑")
			}
			return
		}
		if reply.JobID != "" && reply.Assignment == nil && jobID == "" && !reply.Cancel {
			s.agentStatus(a, "error", "派发回执丢失或已被领取，请核对任务；不会重复执行")
			return
		}
		if reply.Cancel && stopJob != nil {
			stopJob()
		}
		if reply.Assignment != nil && jobID == "" {
			if seen[reply.JobID] {
				s.agentStatus(a, "error", "Host 重复投递已处理任务，已停止以避免重复执行")
				return
			}
			seen[reply.JobID] = true
			jobID = reply.JobID
			assignment := *reply.Assignment
			assignment.ProjectRoot = a.Project
			run, cancel := context.WithTimeout(ctx, 30*time.Minute)
			stopJob = cancel
			results = make(chan outcome, 1)
			output := results
			s.agentStatus(a, "working", "")
			go func() {
				member := workgroup.Member{ID: a.WorkerID, Provider: a.Provider, Mode: a.Mode}
				var r workgroup.Result
				var e error
				if a.WorkspaceScope == "host" {
					if assignment.Workspace == nil {
						e = fmt.Errorf("Host 未提供项目副本，不执行任务")
					} else {
						r, e = workgroup.RunInWorkspace(run, member, assignment)
					}
				} else {
					r, e = (workgroup.NativeProvider{Provider: a.Provider}).Run(run, member, assignment)
				}
				output <- outcome{r, e}
			}()
		}
		select {
		case <-ctx.Done():
			return
		case got := <-results:
			stopJob()
			stopJob = nil
			results = nil
			req := workgroup.WorkerRequest{Action: "result", ID: a.WorkerID, JobID: jobID, Text: got.result.Text, Changes: got.result.Changes, Failed: got.err != nil}
			if got.err != nil {
				req.Text = "本机 Agent 执行未完成，请在本机核对登录、连接和权限"
			}
			if _, e = l.Worker(ctx, req); e != nil {
				s.agentStatus(a, "error", "回执未确认，请核对 Room；不会自动重复执行")
				return
			}
			jobID = ""
			needsCheck = needsCheck || req.Failed
			s.agentStatus(a, "idle", req.Text)
			if !req.Failed {
				lastHealthy = time.Now()
				s.mu.Lock()
				a.LastHealthy = lastHealthy
				s.mu.Unlock()
				s.agentStatus(a, "idle", "")
			}
		case <-ticker.C:
		}
	}
}
