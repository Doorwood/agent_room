package answerwindow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"

	"agent_romm/internal/network"
	"agent_romm/internal/resources"
)

type ResourceFunc func(context.Context, network.ResourceRequest, resources.Execute) (network.ResourceReply, error)

func (w *Window) EnableResources(fn ResourceFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.resource = fn
	if w.personalCancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		w.personalCancel = cancel
		w.personalClient = newPersonalClient()
		w.personalDone = make(chan struct{})
		go func() { defer close(w.personalDone); w.personalLoop(ctx) }()
	}
}
func (w *Window) serveResources(out http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	fn := w.resource
	w.mu.Unlock()
	if fn == nil {
		http.Error(out, "当前连接不支持资源授权，请更新 Host 和客户端", 503)
		return
	}
	if r.Method == "GET" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		info, err := fn(ctx, network.ResourceRequest{Action: "info"}, nil)
		if err != nil {
			http.Error(out, "资源状态不可用，请检查 Host 连接与版本", 503)
			return
		}
		w.mu.Lock()
		enabled, busy := w.resourceEnabled, w.resourceCancel != nil
		w.mu.Unlock()
		_, gh := exec.LookPath("gh")
		_, lark := exec.LookPath("lark-cli")
		out.Header().Set("Content-Type", "application/json")
		json.NewEncoder(out).Encode(map[string]any{"canCreate": info.Mode == "personal" && info.Role == "roommate", "mode": info.Mode, "role": info.Role, "enabled": enabled, "busy": busy, "githubInstalled": gh == nil, "feishuInstalled": lark == nil})
		return
	}
	if r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(out, "invalid origin", 403)
		return
	}
	var in struct {
		Action    string            `json:"action"`
		Enabled   bool              `json:"enabled"`
		Root      string            `json:"root"`
		Confirmed bool              `json:"confirmed"`
		Mode      string            `json:"mode"`
		Resource  resources.Request `json:"resource"`
		Question  string            `json:"question"`
	}
	d := json.NewDecoder(http.MaxBytesReader(out, r.Body, 196608))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(out, "invalid request", 400)
		return
	}
	w.mu.Lock()
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(w.host+"\n"+w.session+"\n"+w.viewer)))
	receiptDir := filepath.Join(filepath.Dir(w.draftDir), "resource-receipts", scope)
	w.mu.Unlock()
	executor := resources.Executor{ReceiptDir: receiptDir}
	if in.Action == "feishu-identity" {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		account, err := executor.FeishuIdentity(ctx)
		if err != nil {
			http.Error(out, err.Error(), 400)
			return
		}
		out.Header().Set("Content-Type", "application/json")
		json.NewEncoder(out).Encode(account)
		return
	}
	if in.Action == "receipt" {
		receipt, err := executor.Receipt(r.Context(), in.Resource.RequestID)
		if err != nil {
			http.Error(out, "暂无本机创建回执；请核对请求标识及当前 Room", 404)
			return
		}
		out.Header().Set("Content-Type", "application/json")
		json.NewEncoder(out).Encode(receipt)
		return
	}
	if in.Action == "grant" {
		if in.Enabled && in.Root != "" && !filepath.IsAbs(in.Root) {
			http.Error(out, "项目目录必须是本机绝对路径", 400)
			return
		}
		w.mu.Lock()
		if w.resourceCancel != nil {
			w.resourceCancel()
		}
		w.resourceEnabled = in.Enabled
		if !in.Enabled {
			w.personalAccount = ""
			w.personalGit = nil
			if w.personalRunCancel != nil {
				w.personalRunCancel()
			}
		}
		w.resourceRoot = ""
		if in.Enabled {
			w.resourceRoot = in.Root
		}
		w.mu.Unlock()
		out.Write([]byte("{}"))
		return
	}
	if in.Action != "query" || !in.Confirmed || in.Resource.Validate() != nil || len(in.Question) > 6000 || (in.Mode != "host" && in.Mode != "personal") {
		http.Error(out, "请确认具体资源操作", 400)
		return
	}
	if in.Resource.IsWrite() && (in.Mode != "personal" || in.Question != "") {
		http.Error(out, "创建文档必须使用个人模式，且不能附带模型执行要求", 403)
		return
	}
	w.mu.Lock()
	if w.resourceCancel != nil || (in.Mode == "personal" && !w.resourceEnabled) {
		w.mu.Unlock()
		http.Error(out, "请先开启个人授权，或等待当前请求结束", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	w.resourceCancel = cancel
	root := w.resourceRoot
	w.mu.Unlock()
	defer func() { cancel(); w.mu.Lock(); w.resourceCancel = nil; w.mu.Unlock() }()
	execute := func(ctx context.Context, req resources.Request) (string, error) {
		return (resources.Executor{Root: root, ReceiptDir: receiptDir}).Run(ctx, req)
	}
	reply, err := fn(ctx, network.ResourceRequest{Action: "query", Mode: in.Mode, Resource: in.Resource, Question: in.Question}, execute)
	if err != nil {
		if in.Resource.IsWrite() {
			if receipt, lookupErr := executor.Receipt(context.Background(), in.Resource.RequestID); lookupErr == nil {
				b, _ := json.Marshal(receipt)
				out.Header().Set("Content-Type", "application/json")
				json.NewEncoder(out).Encode(network.ResourceReply{Type: "done", Mode: "personal", Text: string(b)})
				return
			}
			http.Error(out, "未收到创建确认，请查询本机回执或在自己的飞书中核对。可能尚未执行，也可能结果未知；不会自动重试或使用 Host 账号。", 400)
		} else {
			http.Error(out, "资源调用未完成。请检查登录、授权模式和连接；没有切换身份。", 400)
		}
		return
	}
	out.Header().Set("Content-Type", "application/json")
	json.NewEncoder(out).Encode(reply)
}
