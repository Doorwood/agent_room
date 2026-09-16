package workgroup

import (
	"agent_romm/internal/room"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Remote workers are owned by an authenticated human, and exist only while
// their local Dashboard renews the lease. No executable paths or keys cross it.
type WorkerRequest struct {
	Healthy        bool              `json:"healthy,omitempty"`
	AgentName      string            `json:"agentName,omitempty"`
	WorkspaceScope string            `json:"workspaceScope,omitempty"`
	Changes        []WorkspaceChange `json:"changes,omitempty"`
	Action         string            `json:"action"`
	ID             string            `json:"id"`
	Provider       string            `json:"provider,omitempty"`
	Mode           string            `json:"mode,omitempty"`
	JobID          string            `json:"jobId,omitempty"`
	Text           string            `json:"text,omitempty"`
	Failed         bool              `json:"failed,omitempty"`
}
type WorkerReply struct {
	Name       string      `json:"name,omitempty"`
	ID         string      `json:"id,omitempty"`
	State      string      `json:"state"`
	JobID      string      `json:"jobId,omitempty"`
	Assignment *Assignment `json:"assignment,omitempty"`
	Cancel     bool        `json:"cancel,omitempty"`
	Error      string      `json:"error,omitempty"`
}
type remoteJob struct {
	id              string
	a               Assignment
	claimed, cancel bool
	done            chan Result
	failed          bool
	received        bool
}
type remoteWorker struct {
	healthyUntil time.Time
	healthy      bool
	scope        string
	invitation   string
	uid          room.UID
	member       Member
	lease        time.Time
	job          *remoteJob
}
type RemoteBroker struct {
	root, workspaceDir string
	mu                 sync.Mutex
	workers            map[string]*remoteWorker
}

func NewRemoteBroker() *RemoteBroker { return &RemoteBroker{workers: map[string]*remoteWorker{}} }

// NewProjectRemoteBroker enables isolated mirrors of one Host project.
func NewProjectRemoteBroker(root, dir string) *RemoteBroker {
	b := NewRemoteBroker()
	b.root = root
	b.workspaceDir = dir
	return b
}
func (b *RemoteBroker) Members() []Member {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Member
	for _, w := range b.workers {
		if w.healthy && time.Now().Before(w.healthyUntil) && time.Now().Before(w.lease) {
			out = append(out, w.member)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (b *RemoteBroker) Handle(uid room.UID, req WorkerRequest) (WorkerReply, error) {
	return b.HandleMember(room.Member{UID: uid, Name: fmt.Sprintf("成员%d", uid)}, req)
}

func AgentName(provider, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = map[string]string{"codex": "Codex", "cursor": "Cursor", "claude-code": "Claude Code"}[provider]
	}
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return "", fmt.Errorf("Agent 名称需为 1–40 个字符，不能包含控制字符")
	}
	return name, nil
}

// Member identity comes from the authenticated connection, never the invitation body.
func (b *RemoteBroker) HandleMember(owner room.Member, req WorkerRequest) (WorkerReply, error) {
	uid := owner.UID
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if req.Action == "invite" {
		if req.WorkspaceScope != "" && req.WorkspaceScope != "local" && req.WorkspaceScope != "host" {
			return WorkerReply{}, fmt.Errorf("invalid workspace scope")
		}
		if req.WorkspaceScope == "host" && b.root == "" {
			return WorkerReply{}, fmt.Errorf("Host 不支持项目隔离副本，请更新 Host")
		}
		if uid == 0 || !room.ValidClientMessageID(room.ClientMessageID(req.ID)) || (req.Provider != "cursor" && req.Provider != "claude-code" && req.Provider != "codex") || (req.Mode != "review" && req.Mode != "work") {
			return WorkerReply{}, fmt.Errorf("invalid invitation")
		}
		agentName, err := AgentName(req.Provider, req.AgentName)
		if err != nil {
			return WorkerReply{}, err
		}
		name := strings.TrimSpace(owner.Name)
		if name == "" {
			name = fmt.Sprintf("成员%d", uid)
		}
		name += "-" + agentName
		if len(name) > 120 {
			return WorkerReply{}, fmt.Errorf("组合后的成员名称过长，请缩短 Agent 名称")
		}
		id := fmt.Sprintf("w%d-%s", uid, req.ID[:12])
		if w := b.workers[id]; w != nil {
			if w.member.Name != name || w.scope != req.WorkspaceScope || w.invitation != req.ID || w.uid != uid || w.member.Description != req.Provider || w.member.Mode != req.Mode {
				return WorkerReply{}, fmt.Errorf("invitation conflict")
			}
			if now.After(w.lease) && w.job != nil {
				return WorkerReply{}, fmt.Errorf("过期邀请存在未确认任务，请核对后重新邀请")
			}
			if now.After(w.lease) {
				w.healthy = false
			}
			w.lease = now.Add(90 * time.Second)
			return WorkerReply{ID: id, Name: name, State: "joined"}, nil
		}
		for id, w := range b.workers {
			if now.After(w.lease) && w.job == nil {
				delete(b.workers, id)
			}
		}
		if len(b.workers) >= 32 {
			return WorkerReply{}, fmt.Errorf("Room 本机 Agent 数量已达上限")
		}
		b.workers[id] = &remoteWorker{scope: req.WorkspaceScope, invitation: req.ID, uid: uid, lease: now.Add(90 * time.Second), member: Member{ID: id, Name: name, Provider: "remote", Description: req.Provider, Mode: req.Mode, TimeoutSeconds: 1800}}
		return WorkerReply{ID: id, Name: name, State: "joined"}, nil
	}
	w := b.workers[req.ID]
	if w == nil || w.uid != uid {
		return WorkerReply{}, fmt.Errorf("worker not owned by sender")
	}
	if req.Action == "leave" {
		w.healthy = false
		w.lease = time.Time{}
		if w.job != nil {
			w.job.cancel = true
		}
		return WorkerReply{ID: req.ID, State: "left"}, nil
	}
	if now.After(w.lease) {
		return WorkerReply{}, fmt.Errorf("worker lease expired; invite again")
	}
	w.lease = now.Add(90 * time.Second)
	if req.Action == "health" {
		if w.job != nil {
			return WorkerReply{ID: req.ID, State: "busy"}, nil
		}
		w.healthy = req.Healthy
		w.healthyUntil = now.Add(30 * time.Minute)
		return WorkerReply{ID: req.ID, State: "health-accepted"}, nil
	}
	if req.Action == "result" {
		if len(req.Changes) > 0 && (w.scope != "host" || w.member.Mode != "work" || req.Failed) {
			return WorkerReply{}, fmt.Errorf("当前成员不允许回传文件修改")
		}
		j := w.job
		if j == nil || req.JobID != j.id {
			return WorkerReply{}, fmt.Errorf("unknown assignment receipt")
		}
		if j.received {
			return WorkerReply{}, fmt.Errorf("result already received")
		}
		if len(req.Text) > maxOutput || (!req.Failed && strings.TrimSpace(req.Text) == "") {
			return WorkerReply{}, fmt.Errorf("invalid result")
		}
		select {
		case j.done <- Result{AgentID: req.ID, Text: req.Text, Changes: req.Changes}:
			j.failed = req.Failed
			w.healthy = !req.Failed
			w.healthyUntil = now.Add(30 * time.Minute)
			j.received = true
		default:
			return WorkerReply{}, fmt.Errorf("result already received")
		}
		return WorkerReply{ID: req.ID, State: "received"}, nil
	}
	if req.Action != "poll" {
		return WorkerReply{}, fmt.Errorf("invalid worker operation")
	}
	out := WorkerReply{ID: req.ID, State: "idle"}
	if !w.healthy {
		out.State = "checking"
	}
	if j := w.job; j != nil {
		// Retain Host ownership while saving, without redelivering an acknowledged job.
		if j.received {
			out.State = "finishing"
			return out, nil
		}
		out.State = "working"
		out.JobID = j.id
		if !j.claimed && !j.cancel {
			out.Assignment = &j.a
			j.claimed = true
		}
		out.Cancel = j.cancel
	}
	return out, nil
}
func (b *RemoteBroker) Run(ctx context.Context, m Member, a Assignment) (Result, error) {
	b.mu.Lock()
	w := b.workers[m.ID]
	if w == nil || !w.healthy || time.Now().After(w.healthyUntil) || time.Now().After(w.lease) || w.job != nil {
		b.mu.Unlock()
		return Result{}, fmt.Errorf("本机 Agent 离线或忙碌")
	}
	scope := w.scope
	b.mu.Unlock()
	var original WorkspaceSnapshot
	if scope == "host" {
		var e error
		original, e = SnapshotProject(ctx, b.root, true)
		if e != nil {
			return Result{}, e
		}
		merged := original
		combined := map[string]WorkspaceChange{}
		for _, prior := range a.Prior {
			for _, change := range prior.Changes {
				if old, ok := combined[change.File.Path]; ok {
					if old.Before != change.Before || old.Delete != change.Delete || fileHash(old.File) != fileHash(change.File) {
						return Result{}, fmt.Errorf("依赖成员修改冲突：%s", change.File.Path)
					}
				} else {
					combined[change.File.Path] = change
				}
			}
		}
		changes := []WorkspaceChange{}
		for _, c := range combined {
			changes = append(changes, c)
		}
		merged, e = ApplyWorkspaceChanges(merged, changes)
		if e != nil {
			return Result{}, e
		}
		a.Workspace = &merged
	}
	b.mu.Lock()
	if b.workers[m.ID] != w || !w.healthy || time.Now().After(w.healthyUntil) || time.Now().After(w.lease) || w.job != nil {
		b.mu.Unlock()
		return Result{}, fmt.Errorf("Agent 已离线或忙碌")
	}
	var nonce [16]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		b.mu.Unlock()
		return Result{}, e
	}
	// Personal Host capability instructions must never go to another member's
	// machine. Remote workers get only the accepted human text and public results.
	a.Prompt = a.HumanPrompt + "\n[远程工作约束] 你在受邀成员本机运行，只处理本机获准项目。不得修改工作目录以外的文件；不得调用 Host 个人授权命令、提交、推送或代替发送者操作外部账号。结果回传 Room 等待人类验收。"
	a.ProjectRoot = ""
	a.Instructions = ""
	j := &remoteJob{id: hex.EncodeToString(nonce[:]), a: a, done: make(chan Result, 1)}
	w.job = j
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if w.job == j {
			w.job = nil
		}
		b.mu.Unlock()
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case result := <-j.done:
			b.mu.Lock()
			failed := j.failed
			b.mu.Unlock()
			if failed {
				return Result{}, fmt.Errorf("本机 Agent 未完成工作")
			}
			if scope == "host" {
				if ctx.Err() != nil {
					return Result{}, ctx.Err()
				}
				if m.Mode == "review" && len(result.Changes) > 0 {
					return Result{}, fmt.Errorf("只读 Agent 返回了文件修改，已拒绝")
				}
				after, e := ApplyWorkspaceChanges(*a.Workspace, result.Changes)
				if e != nil {
					return Result{}, e
				}
				changes := WorkspaceDiff(original, after)
				dir, e := MaterializeWorkspace(b.workspaceDir, after)
				if e != nil {
					return Result{}, e
				}
				if e = saveWorkspaceReceipt(dir, original, changes, m.ID, a.ID); e != nil {
					os.RemoveAll(dir)
					return Result{}, e
				}
				result.Changes = changes
				result.WorkspacePath = dir
				result.Text += "\n\nHost 隔离工作副本：" + dir + "\n变更回执：" + filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-receipt.json") + "\n主项目未被覆盖；请在此副本验收。"
			}
			return result, nil
		case <-ctx.Done():
			b.mu.Lock()
			j.cancel = true
			claimed := j.claimed
			b.mu.Unlock()
			if !claimed {
				return Result{}, ctx.Err()
			}
			select {
			case <-j.done:
				return Result{}, ctx.Err()
			case <-time.After(10 * time.Second):
				return Result{}, room.ErrDeliveryUnknown
			}
		case <-ticker.C:
			b.mu.Lock()
			expired := time.Now().After(w.lease)
			claimed := j.claimed
			b.mu.Unlock()
			if expired {
				if claimed {
					return Result{}, room.ErrDeliveryUnknown
				}
				return Result{}, errors.New("本机 Agent 已断开")
			}
		}
	}
}
