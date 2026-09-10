package personal

import (
	"agent_romm/internal/gitpush"
	"agent_romm/internal/room"
	"context"
	"errors"
	"fmt"
)

func (b *Broker) Push(ctx context.Context, capability, root string, in gitpush.Input) (Result, error) {
	b.commitMu.Lock()
	defer b.commitMu.Unlock()
	b.mu.Lock()
	err := b.valid(ctx)
	if capability == "" || capability != b.capability {
		err = errors.New("本轮推送凭证失效")
	}
	// At most one immutable pack per turn. Retries reuse the claimed request and receipt.
	var previous *pending
	for _, p := range b.pending {
		if p.Action == "git.push" {
			previous = p
			break
		}
	}
	b.mu.Unlock()
	if err != nil {
		return Result{}, err
	}
	if err = in.Validate(); err != nil {
		return Result{}, err
	}
	title := "推送 " + in.Repository + " · " + in.Branch
	content := fmt.Sprintf("目标仓库：https://github.com/%s\n目标分支：%s\n固定提交：%s\n将传送该提交及其可达历史。仅允许快进或创建分支，不覆盖远端历史。", in.Repository, in.Branch, in.Commit)
	if previous != nil {
		if previous.Push == nil || previous.Push.Input != in {
			return Result{}, errors.New("每轮仅允许一个明确的推送目标，请另发消息")
		}
		return b.callPrepared(ctx, capability, "git.push", title, content, previous.Push, nil)
	}
	offer, pack, err := gitpush.Prepare(ctx, root, in)
	if err != nil {
		return Result{}, err
	}
	return b.callPrepared(ctx, capability, "git.push", title, content, &offer, pack)
}

// PushChunk is only reachable through the approved sender's pinned TLS connection.
func (b *Broker) PushChunk(ctx context.Context, uid room.UID, client, id string, offset int) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.valid(ctx); err != nil {
		return nil, err
	}
	p := b.pending[id]
	if p == nil || p.Action != "git.push" || p.uid != uid || p.client == "" || p.client != client || p.result != nil || offset < 0 || offset >= len(p.pack) || offset%gitpush.ChunkSize != 0 {
		return nil, errors.New("提交包不属于本次发送者请求或已经失效")
	}
	end := offset + gitpush.ChunkSize
	if end > len(p.pack) {
		end = len(p.pack)
	}
	return append([]byte(nil), p.pack[offset:end]...), nil
}
