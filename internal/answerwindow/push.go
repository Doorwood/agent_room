package answerwindow

import (
	"agent_romm/internal/gitpush"
	"agent_romm/internal/network"
	"agent_romm/internal/personal"
	"context"
	"errors"
	"time"
)

func (w *Window) personalPushStep(ctx context.Context, p *personal.Request, fn ResourceFunc, client string) {
	if p.Push == nil || p.Push.Validate() != nil {
		w.mu.Lock()
		w.personalStatus = "无效的 GitHub 推送请求"
		w.mu.Unlock()
		return
	}
	w.mu.Lock()
	executor := gitpush.Executor{ReceiptDir: w.personalExecutorLocked().ReceiptDir}
	approval := w.personalPush
	granted := approval != nil && approval.ID == p.ID && w.personalPushGranted == p.ID && gitpush.SameOffer(approval.Offer, *p.Push)
	w.mu.Unlock()
	// A saved receipt is returned without executing any remote operation again.
	saved, err := executor.Receipt(ctx, p.ID, *p.Push)
	if err != nil {
		w.mu.Lock()
		w.personalStatus = "本机推送回执不可用，已停止；不会重新推送"
		w.mu.Unlock()
		return
	}
	if saved != nil {
		w.deliverPush(ctx, fn, client, p.ID, *saved)
		return
	}
	if !granted {
		w.mu.Lock()
		if w.personalPushFailed != p.ID {
			w.personalStatus = "推送正在等待本人核对 GitHub 账号、仓库、分支和 commit。"
		}
		w.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	w.mu.Lock()
	if w.personalPush != approval || w.personalPushGranted != p.ID {
		w.mu.Unlock()
		cancel()
		return
	}
	w.personalRunCancel = cancel
	w.personalStatus = "正在通过本机 GitHub 账号推送…"
	w.mu.Unlock()
	defer func() { cancel(); w.mu.Lock(); w.personalRunCancel = nil; w.mu.Unlock() }()
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
				if e != nil || current.Mode != "personal" || current.Role != "roommate" || current.Personal == nil || current.Personal.ID != p.ID || current.Personal.Push == nil || !gitpush.SameOffer(*current.Personal.Push, *p.Push) {
					cancel()
					return
				}
			}
		}
	}()
	pack := make([]byte, 0, p.Push.PackSize)
	for len(pack) < p.Push.PackSize {
		chunkCtx, stop := context.WithTimeout(runCtx, 10*time.Second)
		reply, e := fn(chunkCtx, network.ResourceRequest{Action: "personal-push-chunk", ClientID: client, PushID: p.ID, PushOffset: len(pack)}, nil)
		stop()
		expected := gitpush.ChunkSize
		if left := p.Push.PackSize - len(pack); left < expected {
			expected = left
		}
		if e != nil || reply.Mode != "personal" || reply.Role != "roommate" || len(reply.PushData) != expected {
			err = errors.New("提交包传输中断或连接权限已变化，未推送")
			break
		}
		pack = append(pack, reply.PushData...)
	}
	var receipt gitpush.Receipt
	if err == nil {
		receipt, err = executor.Execute(runCtx, approval, pack)
	}
	cancel()
	<-monitorDone
	if err != nil {
		w.mu.Lock()
		w.personalPush = nil
		w.personalPushGranted = ""
		w.personalPushFailed = p.ID
		w.personalStatus = err.Error() + "；请重新核对授权。"
		w.mu.Unlock()
		return
	}
	w.deliverPush(ctx, fn, client, p.ID, receipt)
}
func (w *Window) deliverPush(ctx context.Context, fn ResourceFunc, client, id string, r gitpush.Receipt) {
	resultCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, err := fn(resultCtx, network.ResourceRequest{Action: "personal-result", ClientID: client, PersonalResult: &personal.Result{ID: id, PushReceipt: &r}}, nil)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.personalPushLast = &r
	w.personalPush = nil
	w.personalPushGranted = ""
	if err != nil {
		w.personalStatus = "本机已保存推送回执，等待 Host 确认；不会重复执行"
		return
	}
	w.personalPending = nil
	if r.State == "completed" {
		w.personalStatus = "已使用你的 GitHub 账号完成推送，结果已返回模型。"
	} else {
		w.personalStatus = "推送结果待核实，请在资源授权中查看本机回执；不会自动重试。"
	}
}
