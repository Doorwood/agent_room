// Package personal binds model-requested writes to the authenticated turn sender.
package personal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"agent_romm/internal/gitpush"
	"agent_romm/internal/resources"
	"agent_romm/internal/room"
)

type Request struct {
	Target  string         `json:"target,omitempty"`
	Push    *gitpush.Offer `json:"push,omitempty"`
	Action  string         `json:"action,omitempty"`
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Content string         `json:"content"`
	Sender  string         `json:"sender"`
}
type Result struct {
	PushReceipt *gitpush.Receipt         `json:"pushReceipt,omitempty"`
	GitIdentity *GitIdentity             `json:"gitIdentity,omitempty"`
	ID          string                   `json:"id"`
	Receipt     *resources.CreateReceipt `json:"receipt,omitempty"`
	Error       string                   `json:"error,omitempty"`
}
type pending struct {
	pack []byte
	Request
	uid    room.UID
	client string
	result *Result
	ready  chan struct{}
}
type Broker struct {
	commitMu     sync.Mutex
	commitCancel context.CancelFunc
	mu           sync.Mutex
	mode         string
	capability   string
	message      room.ClientMessageID
	actor        room.Actor
	pending      map[string]*pending
	check        func(context.Context, room.UID, room.ClientMessageID) error
}

func New(check func(context.Context, room.UID, room.ClientMessageID) error) *Broker {
	return &Broker{mode: "host", pending: map[string]*pending{}, check: check}
}
func (b *Broker) Mode() string { b.mu.Lock(); defer b.mu.Unlock(); return b.mode }
func (b *Broker) invalidateLocked() {
	b.capability = ""
	if b.commitCancel != nil {
		b.commitCancel()
		b.commitCancel = nil
	}
	for _, p := range b.pending {
		if p.result == nil {
			r := Result{ID: p.ID, Error: "本轮已结束、中断或授权模式已改变；没有改用 Host。若创建已经开始，请查看本机回执。"}
			p.result = &r
			close(p.ready)
		}
	}
	b.pending = map[string]*pending{}
}
func (b *Broker) Invalidate() { b.mu.Lock(); defer b.mu.Unlock(); b.invalidateLocked() }
func (b *Broker) SetMode(mode string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mode != mode {
		b.invalidateLocked()
		b.mode = mode
	}
}
func (b *Broker) Bind(id room.ClientMessageID, actor room.Actor) (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.invalidateLocked()
	b.message = id
	b.actor = actor
	if b.mode != "personal" {
		return "", nil
	}
	b.capability = hex.EncodeToString(nonce[:])
	return b.capability, nil
}
func (b *Broker) valid(ctx context.Context) error {
	if b.mode != "personal" || b.capability == "" {
		return errors.New("个人创建通道不可用；禁止回退 Host")
	}
	if b.check != nil {
		return b.check(ctx, b.actor.UID, b.message)
	}
	return nil
}
func (b *Broker) Call(ctx context.Context, capability, title, content string) (Result, error) {
	return b.call(ctx, capability, "feishu.create", title, content)
}
func (b *Broker) call(ctx context.Context, capability, action, title, content string) (Result, error) {
	return b.callPrepared(ctx, capability, action, title, content, nil, nil)
}
func (b *Broker) callPrepared(ctx context.Context, capability, action, title, content string, offer *gitpush.Offer, pack []byte) (Result, error) {
	return b.callTarget(ctx, capability, action, title, content, "", offer, pack)
}
func (b *Broker) Append(ctx context.Context, capability, target, title, content string) (Result, error) {
	return b.callTarget(ctx, capability, "feishu.append", title, content, target, nil, nil)
}
func (b *Broker) callTarget(ctx context.Context, capability, action, title, content, target string, offer *gitpush.Offer, pack []byte) (Result, error) {
	b.mu.Lock()
	if capability == "" || capability != b.capability {
		b.mu.Unlock()
		return Result{}, errors.New("创建请求不属于当前发送者或本轮凭证已失效")
	}
	if err := b.valid(ctx); err != nil {
		b.mu.Unlock()
		return Result{}, err
	}
	sum := sha256.Sum256([]byte(string(b.message) + "\x00" + action + "\x00" + title + "\x00" + content + "\x00" + target))
	id := fmt.Sprintf("%x", sum[:16])
	r := resources.Request{Action: action, Target: target, Title: title, Content: content, RequestID: id, AccountID: "ou_validation000"}
	if action != "git.commit" && action != "git.push" {
		if err := r.Validate(); err != nil {
			b.mu.Unlock()
			return Result{}, err
		}
	}
	p := b.pending[id]
	if p == nil {
		if len(b.pending) >= 8 {
			b.mu.Unlock()
			return Result{}, errors.New("本轮创建请求数量已达上限")
		}
		p = &pending{pack: pack, Request: Request{Target: target, Push: offer, Action: action, ID: id, Title: title, Content: content, Sender: b.actor.Name}, uid: b.actor.UID, ready: make(chan struct{})}
		b.pending[id] = p
	}
	b.mu.Unlock()
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	select {
	case <-p.ready:
		b.mu.Lock()
		result := *p.result
		b.mu.Unlock()
		return result, nil
	case <-ctx.Done():
		b.mu.Lock()
		if p.result == nil {
			r := Result{ID: id, Error: "请求已中断；可能已开始创建，请检查本机回执，禁止重复创建或使用 Host。"}
			p.result = &r
			close(p.ready)
		}
		b.mu.Unlock()
		return Result{}, ctx.Err()
	case <-timer.C:
		b.mu.Lock()
		if p.result == nil {
			r := Result{ID: id, Error: "等待发送者授权超时；原请求已停止。请询问发送者检查本机回执，禁止回退 Host 或自动重新创建。"}
			p.result = &r
			close(p.ready)
		}
		result := *p.result
		b.mu.Unlock()
		return result, nil

	}
}

// Next atomically claims a request for exactly one authenticated client instance.
// A disconnected claimant is never replaced: it may already have performed the write.
func (b *Broker) Next(ctx context.Context, uid room.UID, client string) (*Request, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(client) != 32 {
		return nil, errors.New("invalid client instance")
	}
	if err := b.valid(ctx); err != nil {
		return nil, nil
	}
	if uid != b.actor.UID {
		return nil, nil
	}
	for _, p := range b.pending {
		if p.uid == uid && p.result == nil && p.client == client {
			r := p.Request
			return &r, nil
		}
	}
	for _, p := range b.pending {
		if p.uid == uid && p.result == nil && (p.client == "" || p.client == client) {
			p.client = client
			r := p.Request
			return &r, nil
		}
	}
	return nil, nil
}
func (b *Broker) Resolve(ctx context.Context, uid room.UID, client string, result Result) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.valid(ctx); err != nil {
		return err
	}
	p := b.pending[result.ID]
	if p == nil || p.uid != uid || p.client == "" || p.client != client {
		return errors.New("创建回执不属于本客户端")
	}
	if p.result != nil {
		return nil
	}
	if result.Error != "" && (result.GitIdentity != nil || result.Receipt != nil || result.PushReceipt != nil) {
		return errors.New("ambiguous result")
	}
	if p.Action == "git.push" && result.PushReceipt != nil && result.GitIdentity == nil && result.Receipt == nil {
		if p.Push == nil || !result.PushReceipt.Valid(p.ID, p.Push.Input) {
			return errors.New("invalid push receipt")
		}
		copy := *result.PushReceipt
		result.PushReceipt = &copy
	} else if p.Action == "git.commit" && result.GitIdentity != nil && result.Receipt == nil && result.PushReceipt == nil {
		if err := result.GitIdentity.Validate(); err != nil {
			return err
		}
		copy := *result.GitIdentity
		result.GitIdentity = &copy
		result.Error = ""
	} else if (p.Action == "feishu.create" || p.Action == "feishu.append" || p.Action == "") && result.GitIdentity == nil && result.Receipt != nil && result.PushReceipt == nil {
		r := result.Receipt
		if r.RequestID != p.ID || r.Title != p.Title || (r.State != "completed" && r.State != "unknown") || r.Account.OpenID == "" {
			return errors.New("invalid creation receipt")
		}
		if p.Action == "feishu.append" && (r.Target != p.Target || r.ContentSHA256 != resources.ContentDigest(p.Content) || r.Action != p.Action || (r.State == "completed" && r.URL != p.Target)) {
			return errors.New("invalid append receipt")
		}
		// Round-trip the typed receipt: arbitrary CLI fields and credentials are excluded.
		encoded, _ := json.Marshal(r)
		var clean resources.CreateReceipt
		json.Unmarshal(encoded, &clean)
		result.Receipt = &clean
		result.Error = ""
	} else if result.Error != "" {
		result.GitIdentity = nil
		result.PushReceipt = nil
		result.Receipt = nil
		result.Error = "发送者尚未授权、拒绝创建或本机创建失败。请询问该发送者处理；禁止使用 Host 或其他成员账号。"
	} else {
		return errors.New("missing result")
	}
	p.result = &result
	close(p.ready)
	return nil
}

func (b *Broker) InvalidateSender(uid room.UID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.actor.UID == uid {
		b.invalidateLocked()
	}
}

// ChangeMode serializes persistence and the active-work check with turn binding.
// A turn starting during the switch therefore receives the new identity policy.
func (b *Broker) ChangeMode(mode string, change func() error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := change(); err != nil {
		return err
	}
	if b.mode != mode {
		b.invalidateLocked()
		b.mode = mode
	}
	return nil
}
