// Package questions stores a separate view of Q&A. Production generation reuses
// the host Codex thread; this database is not a separate model session.
package questions

import (
	"agent_romm/internal/room"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	_ "modernc.org/sqlite"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	CreatedAt   string   `json:"createdAt,omitempty"`
	CompletedAt string   `json:"completedAt,omitempty"`
	Seq         int64    `json:"seq"`
	Session     string   `json:"session"`
	UID         room.UID `json:"uid"`
	Name        string   `json:"name"`
	Question    string   `json:"question"`
	Answer      string   `json:"answer"`
	State       string   `json:"state"`
}
type SharedRunner func(context.Context, room.Actor, room.ClientMessageID, string) (room.QuestionAnswer, error)

type Service struct {
	runner SharedRunner
	db     *sql.DB
	mu     sync.Mutex
}

func Open(dir string, runner SharedRunner) (*Service, error) {
	db, err := sql.Open("sqlite", filepath.Join(dir, "questions.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE IF NOT EXISTS questions(seq INTEGER PRIMARY KEY AUTOINCREMENT,uid INTEGER NOT NULL,name TEXT NOT NULL,request_id TEXT NOT NULL,session TEXT NOT NULL,question TEXT NOT NULL,answer TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,UNIQUE(uid,request_id));UPDATE questions SET state='failed',answer='host 重启，请重新提问' WHERE state='running';`)
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, column := range []string{"created_at", "completed_at"} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('questions') WHERE name=?", column).Scan(&count); err != nil {
			db.Close()
			return nil, err
		}
		if count == 0 {
			if _, err = db.Exec("ALTER TABLE questions ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	return &Service{db: db, runner: runner}, nil
}
func (s *Service) Close() error { return s.db.Close() }
func (s *Service) Ready() bool  { return s.runner != nil }
func (s *Service) List(ctx context.Context, uid room.UID, all bool, before int64) ([]Entry, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,session,uid,name,question,answer,state,created_at,completed_at FROM questions WHERE (? OR uid=?) AND seq<? ORDER BY seq DESC LIMIT 50`, all, uid, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Seq, &e.Session, &e.UID, &e.Name, &e.Question, &e.Answer, &e.State, &e.CreatedAt, &e.CompletedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
func (s *Service) Ask(ctx context.Context, m room.Member, id, text string) (Entry, error) {
	if !s.Ready() {
		return Entry{}, errors.New("Host 尚未提供 Codex 只读问答")
	}
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 16 || len(text) > 6000 || strings.TrimSpace(text) == "" {
		return Entry{}, errors.New("问题不能为空且最多 6000 字节")
	}
	// Single in-flight generation bounds host resource use and preserves per-user ordering.
	if !s.mu.TryLock() {
		return Entry{}, errors.New("问答正在处理请求，请稍后重试")
	}
	defer s.mu.Unlock()
	var e Entry
	err = s.db.QueryRowContext(ctx, `SELECT seq,session,uid,name,question,answer,state,created_at,completed_at FROM questions WHERE uid=? AND request_id=?`, m.UID, id).Scan(&e.Seq, &e.Session, &e.UID, &e.Name, &e.Question, &e.Answer, &e.State, &e.CreatedAt, &e.CompletedAt)
	if err == nil {
		if e.Question != text {
			return Entry{}, errors.New("request ID conflict")
		}
		return e, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return e, err
	}

	e.UID = m.UID
	e.Name = m.Name
	e.Question = text
	e.State = "running"
	e.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO questions(uid,name,request_id,session,question,state,created_at)VALUES(?,?,?,?,?,'running',?)`, m.UID, m.Name, id, e.Session, text, e.CreatedAt)
	if err != nil {
		return e, err
	}
	e.Seq, _ = result.LastInsertId()
	answer, modelErr := s.runner(ctx, room.Actor{UID: m.UID, Name: m.Name}, room.ClientMessageID(id), text)
	e.Session = answer.Session
	e.State = "completed"
	e.Answer = answer.Text
	if modelErr != nil {
		e.State = "failed"
		e.Answer = modelErr.Error()
	}
	if len(e.Answer) > 32000 {
		e.Answer = e.Answer[:32000]
	}
	// Persist a terminal outcome even if this individual request was disconnected.
	e.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`UPDATE questions SET answer=?,state=?,session=?,completed_at=? WHERE seq=?`, e.Answer, e.State, e.Session, e.CompletedAt, e.Seq)
	return e, err
}
