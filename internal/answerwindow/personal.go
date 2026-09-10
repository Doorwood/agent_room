package answerwindow

import (
	"agent_romm/internal/gitpush"
	"agent_romm/internal/network"
	"agent_romm/internal/personal"
	"agent_romm/internal/resources"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

func newPersonalClient() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
func (w *Window) personalExecutorLocked() resources.Executor {
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(w.host+"\n"+w.session+"\n"+w.viewer)))
	return resources.Executor{ReceiptDir: filepath.Join(filepath.Dir(w.draftDir), "resource-receipts", scope)}
}
func (w *Window) personalLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.personalStep(ctx)
		}
	}
}
func (w *Window) personalStep(ctx context.Context) {
	w.mu.Lock()
	fn, client := w.resource, w.personalClient
	w.mu.Unlock()
	if fn == nil {
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	reply, err := fn(queryCtx, network.ResourceRequest{Action: "personal-pending", ClientID: client}, nil)
	cancel()
	if err != nil || reply.Mode != "personal" || reply.Role != "roommate" {
		w.mu.Lock()
		w.personalPending = nil
		w.personalAccount = ""
		w.personalAppendGranted = ""
		w.personalGit = nil
		w.personalPush = nil
		w.personalPushGranted = ""
		w.mu.Unlock()
		return
	}
	p := reply.Personal
	w.mu.Lock()
	w.personalPending = p
	if p == nil {
		w.personalPush = nil
		w.personalPushGranted = ""
		w.mu.Unlock()
		return
	}
	if p.Action == "git.push" {
		w.mu.Unlock()
		w.personalPushStep(ctx, p, fn, client)
		return
	}
	if p.Action == "git.commit" {
		identity := w.personalGit
		if identity == nil {
			w.personalStatus = "你要求的代码提交正在等待 Git 署名确认，请本人填写姓名和邮箱。"
			w.mu.Unlock()
			return
		}
		copy := *identity
		w.mu.Unlock()
		resultCtx, done := context.WithTimeout(ctx, 8*time.Second)
		defer done()
		_, e := fn(resultCtx, network.ResourceRequest{Action: "personal-result", ClientID: client, PersonalResult: &personal.Result{ID: p.ID, GitIdentity: &copy}}, nil)
		w.mu.Lock()
		if e == nil {
			w.personalPending = nil
			w.personalStatus = "已将本人确认的 Git 署名交给本次提交，结果由模型回复。"
		}
		w.mu.Unlock()
		return
	}
	if p.Action != "" && p.Action != "feishu.create" && p.Action != "feishu.append" {
		w.personalStatus = "不支持该个人操作，请更新客户端"
		w.mu.Unlock()
		return
	}
	account := w.personalAccount
	if account == "" || (p.Action == "feishu.append" && w.personalAppendGranted != p.ID) {
		w.personalStatus = "你要求创建的飞书文档正在等待本机授权。请核对账号后继续；不会使用 Host 账号。"
		w.mu.Unlock()
		return
	}
	executor := w.personalExecutorLocked()
	runCtx, runCancel := context.WithTimeout(ctx, 4*time.Minute)
	w.personalRunCancel = runCancel
	w.personalStatus = "正在使用你的个人飞书账号写入并核验正文…"
	w.mu.Unlock()
	defer func() { runCancel(); w.mu.Lock(); w.personalRunCancel = nil; w.mu.Unlock() }()
	action := p.Action
	if action == "" {
		action = "feishu.create"
	}
	req := resources.Request{Action: action, Target: p.Target, Title: p.Title, Content: p.Content, RequestID: p.ID, AccountID: account}
	// While the CLI runs, loss of the originating request cancels local work.
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-tick.C:
				checkCtx, stop := context.WithTimeout(runCtx, 3*time.Second)
				current, e := fn(checkCtx, network.ResourceRequest{Action: "personal-pending", ClientID: client}, nil)
				stop()
				if e != nil || current.Personal == nil || current.Personal.ID != p.ID || current.Mode != "personal" || current.Role != "roommate" {
					runCancel()
					return
				}
			}
		}
	}()
	text, err := executor.Run(runCtx, req)
	runCancel()
	<-monitorDone
	var receipt resources.CreateReceipt
	if err == nil {
		err = json.Unmarshal([]byte(text), &receipt)
	}
	if err != nil {
		w.mu.Lock()
		w.personalAccount = ""
		w.personalAppendGranted = ""
		w.personalStatus = "本机飞书登录、账号或权限不可用，请重新核对并授权。不会使用 Host 或其他成员的账号。"
		w.mu.Unlock()
		return
	}
	result := personal.Result{ID: p.ID, Receipt: &receipt}
	resultCtx, done := context.WithTimeout(ctx, 8*time.Second)
	defer done()
	_, err = fn(resultCtx, network.ResourceRequest{Action: "personal-result", ClientID: client, PersonalResult: &result}, nil)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.personalStatus = "本机已保存创建回执，等待 Host 确认；不会重复创建。"
		return
	}
	w.personalPending = nil
	if receipt.State == "completed" {
		w.personalStatus = "已用你的个人飞书账号写入并核验正文，结果已返回模型。"
	} else {
		w.personalStatus = "创建结果待核实，本机已保存回执；不会自动重试。"
	}
}
func (w *Window) servePersonal(out http.ResponseWriter, r *http.Request) {
	out.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" {
		w.mu.Lock()
		defer w.mu.Unlock()
		json.NewEncoder(out).Encode(map[string]any{"pending": w.personalPending, "status": w.personalStatus, "accountId": w.personalAccount, "appendApproved": w.personalPending != nil && w.personalAppendGranted == w.personalPending.ID, "gitIdentity": w.personalGit, "busy": w.personalRunCancel != nil, "pushApproved": w.personalPending != nil && w.personalPushGranted == w.personalPending.ID, "lastPush": w.personalPushLast})
		return
	}
	if r.Method != "POST" || r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(out, "invalid origin or method", 403)
		return
	}
	var in struct {
		GitHubID    int64                 `json:"githubId,omitempty"`
		GitIdentity *personal.GitIdentity `json:"gitIdentity,omitempty"`
		Action      string                `json:"action"`
		ID          string                `json:"id"`
		AccountID   string                `json:"accountId"`
	}
	d := json.NewDecoder(http.MaxBytesReader(out, r.Body, 2048))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(out, "invalid request", 400)
		return
	}
	w.mu.Lock()
	p := w.personalPending
	fn, client := w.resource, w.personalClient
	busy := w.personalRunCancel != nil
	executor := w.personalExecutorLocked()
	w.mu.Unlock()
	if in.Action == "document-history" {
		items, err := executor.RecentDocuments(r.Context())
		if err != nil {
			http.Error(out, "本机文档回执不可用", 409)
			return
		}
		json.NewEncoder(out).Encode(items)
		return
	}
	if in.Action == "push-history" {
		items, err := (gitpush.Executor{ReceiptDir: executor.ReceiptDir}).Recent(r.Context())
		if err != nil {
			http.Error(out, "本机推送回执不可用", 409)
			return
		}
		json.NewEncoder(out).Encode(items)
		return
	}
	if p == nil || p.ID != in.ID || fn == nil || busy {
		http.Error(out, "请求已变化或正在创建，请刷新查看", 409)
		return
	}
	if in.Action == "push-check" {
		if p.Action != "git.push" || p.Push == nil {
			http.Error(out, "请求不是 GitHub 推送", 400)
			return
		}
		approval, err := (gitpush.Executor{ReceiptDir: executor.ReceiptDir}).Check(r.Context(), p.ID, *p.Push)
		if err != nil {
			http.Error(out, err.Error(), 409)
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.personalPending == nil || w.personalPending.ID != in.ID || w.personalPending.Push == nil || !gitpush.SameOffer(*w.personalPending.Push, approval.Offer) {
			http.Error(out, "请求已变化", 409)
			return
		}
		w.personalPushFailed = ""
		w.personalPush = approval
		w.personalPushGranted = ""
		json.NewEncoder(out).Encode(approval)
		return
	} else if in.Action == "push-grant" {
		w.mu.Lock()
		defer w.mu.Unlock()
		if p.Action != "git.push" || w.personalPending == nil || w.personalPending.ID != in.ID || w.personalPush == nil || w.personalPush.ID != in.ID || w.personalPush.Account.ID != in.GitHubID {
			http.Error(out, "请重新核对本机 GitHub 账号和推送目标", 409)
			return
		}
		w.personalPushGranted = in.ID
		w.personalStatus = "已确认本次推送，正在下载并校验提交包。"
	} else if in.Action == "git-grant" {
		if p.Action != "git.commit" || in.GitIdentity == nil || in.GitIdentity.Validate() != nil {
			http.Error(out, "请填写有效的 Git 姓名和邮箱", 400)
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.personalPending == nil || w.personalPending.ID != in.ID {
			http.Error(out, "请求已失效", 409)
			return
		}
		identity := *in.GitIdentity
		w.personalGit = &identity
		w.personalStatus = "已确认本次连接的 Git 署名，正在继续提交。"
	} else if in.Action == "grant" {
		if p.Action != "feishu.create" && p.Action != "feishu.append" && p.Action != "" {
			http.Error(out, "Git 提交需要单独确认姓名和邮箱，不能复用飞书授权", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		identity, err := executor.FeishuIdentity(ctx)
		if err != nil || identity.OpenID != in.AccountID {
			http.Error(out, "本机账号不可用或已变化，请重新核对账号后授权", 409)
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.personalPending == nil || w.personalPending.ID != in.ID {
			http.Error(out, "请求已失效", 409)
			return
		}
		w.personalAccount = identity.OpenID
		if p.Action == "feishu.append" {
			w.personalAppendGranted = p.ID
		}
		w.resourceEnabled = true
		w.personalStatus = "已授权本次连接使用该账号创建你要求的飞书文档，正在继续。"
	} else if in.Action == "decline" {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		_, err := fn(ctx, network.ResourceRequest{Action: "personal-result", ClientID: client, PersonalResult: &personal.Result{ID: p.ID, Error: "declined"}}, nil)
		if err != nil {
			http.Error(out, "请求已失效或未收到确认", 409)
			return
		}
		w.mu.Lock()
		w.personalPending = nil
		w.personalPush = nil
		w.personalPushGranted = ""
		w.personalStatus = "已拒绝本次操作；模型不会得到执行成功的结果。"
		w.mu.Unlock()
	} else {
		http.Error(out, "invalid action", 400)
		return
	}
	out.Write([]byte("{}"))
}
