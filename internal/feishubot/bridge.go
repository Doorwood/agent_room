package feishubot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Event struct {
	Type        string `json:"type"`
	EventID     string `json:"event_id"`
	MessageID   string `json:"message_id"`
	SenderID    string `json:"sender_id"`
	ChatID      string `json:"chat_id"`
	ChatType    string `json:"chat_type"`
	MessageType string `json:"message_type"`
	Content     string `json:"content"`
	CreateTime  string `json:"create_time"`
}
type Backend interface {
	Authorize(context.Context, uint32, bool) error
	Status(context.Context) (string, error)
	History(context.Context, string) (string, error)
	Work(context.Context, uint32, string, string) (string, error)
}
type Transport interface {
	Send(context.Context, string, string, string) error
	ChatHistory(context.Context, string) (string, error)
}
type Bridge struct {
	Config    Config
	Backend   Backend
	Transport Transport
	db        *sql.DB
}

func New(c Config, dir string, b Backend, t Transport) (*Bridge, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if b == nil || t == nil {
		return nil, errors.New("missing bot adapters")
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("bot state must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("bot state must be private")
	}
	path := filepath.Join(dir, "events.db")
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		return nil, errors.New("invalid bot ledger")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS events(id TEXT PRIMARY KEY,digest TEXT NOT NULL,state TEXT NOT NULL,created TEXT NOT NULL,sender TEXT NOT NULL);`)
	if err == nil {
		err = os.Chmod(path, 0600)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Bridge{c, b, t, db}, nil
}
func (b *Bridge) Close() error { return b.db.Close() }
func (b *Bridge) Handle(ctx context.Context, e Event) error {
	if e.Type != "im.message.receive_v1" || e.MessageType != "text" || !openID.MatchString(e.SenderID) || !messageID.MatchString(e.MessageID) || !chatID.MatchString(e.ChatID) || len(e.Content) > 16000 || !utf8.ValidString(e.Content) {
		return nil
	}
	ms, err := strconv.ParseInt(e.CreateTime, 10, 64)
	if err != nil || time.Since(time.UnixMilli(ms)) > 10*time.Minute || time.Until(time.UnixMilli(ms)) > time.Minute {
		return nil
	}
	var binding *Binding
	for i := range b.Config.Bindings {
		if b.Config.Bindings[i].OpenID == e.SenderID {
			binding = &b.Config.Bindings[i]
			break
		}
	}
	if binding == nil {
		return nil
	}
	if e.ChatType != "p2p" && (e.ChatType != "group" || !contains(binding.OriginChats, e.ChatID)) {
		return nil
	}
	text := strings.TrimSpace(e.Content)
	// Group @ rendering differs across CLI versions. Only a literal /room
	// command is actionable; never interpret arbitrary conversation as work.
	if i := strings.Index(text, "/room "); i > 0 && strings.HasPrefix(text, "@") {
		text = text[i:]
	}
	if text != "/room" && !strings.HasPrefix(text, "/room ") {
		return nil
	}
	if err = b.Backend.Authorize(ctx, binding.UID, false); err != nil {
		return nil
	}
	raw, _ := json.Marshal(e)
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(b.Config.AppID+"\x00"+e.MessageID)))
	var recent int
	if err = b.db.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE sender=? AND created>?", e.SenderID, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)).Scan(&recent); err != nil {
		return err
	}
	if recent >= 10 {
		return nil
	}
	result, err := b.db.ExecContext(ctx, "INSERT OR IGNORE INTO events VALUES(?,?,?,?,?)", id, digest, "reserved", time.Now().UTC().Format(time.RFC3339Nano), e.SenderID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil
	}
	// Entries older than the replay window can be pruned without re-executing
	// old events: their creation time is rejected before this point.
	if _, err = b.db.ExecContext(ctx, "DELETE FROM events WHERE created<?", time.Now().Add(-30*24*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	response, workErr := b.execute(ctx, *binding, id[:32], strings.TrimSpace(strings.TrimPrefix(text, "/room")))
	if workErr != nil {
		response = "操作未完成，可能需要检查权限或连接。派工结果不明时请先在 Room 核实，勿重复提交。\n请求：" + id[:32]
	}
	// Check role/revocation again before disclosing Room or group content.
	if err = b.Backend.Authorize(ctx, binding.UID, false); err != nil {
		return err
	}
	if len([]rune(response)) > 6000 {
		response = string([]rune(response)[:6000]) + "\n…内容已截断，请在 Room 查看完整结果。"
	}
	err = b.Transport.Send(ctx, e.SenderID, b.Config.Name+"\n"+response, id[:32])
	state := "completed"
	if err != nil || workErr != nil {
		state = "unknown"
	}
	save, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, saveErr := b.db.ExecContext(save, "UPDATE events SET state=? WHERE id=?", state, id)
	if err != nil {
		return err
	}
	return saveErr
}
func (b *Bridge) execute(ctx context.Context, u Binding, id, command string) (string, error) {
	switch {
	case command == "" || command == "帮助" || command == "help":
		return "我是此项目的 Codex 虚拟成员。\n/room 状态\n/room 历史 [关键词]\n/room 工作 <明确要求>\n/room 群消息 <oc_群ID>\n结果只私聊回复发送者。工作进入项目共享队列，不自动提交或推送代码。", nil
	case command == "状态" || command == "status":
		return b.Backend.Status(ctx)
	case command == "历史" || strings.HasPrefix(command, "历史 "):
		return b.Backend.History(ctx, strings.TrimSpace(strings.TrimPrefix(command, "历史")))
	case strings.HasPrefix(command, "工作 "):
		body := strings.TrimSpace(strings.TrimPrefix(command, "工作 "))
		if !u.AllowWork || body == "" {
			return "未启用你的飞书派工权限，请由 Host 维护者核对绑定；查询权限不代表派工权限。", nil
		}
		if err := b.Backend.Authorize(ctx, u.UID, true); err != nil {
			return "当前 Room 身份不能安排工作。", nil
		}
		return b.Backend.Work(ctx, u.UID, id, body)
	case strings.HasPrefix(command, "群消息 "):
		ch := strings.TrimSpace(strings.TrimPrefix(command, "群消息 "))
		if !chatID.MatchString(ch) || !contains(u.ReadableChats, ch) {
			return "该群未在你的可读群列表中，不能读取。", nil
		}
		return b.Transport.ChatHistory(ctx, ch)
	default:
		return "请使用 /room 帮助 查看支持的命令；普通聊天不会自动派工。", nil
	}
}
