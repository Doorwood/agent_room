package answerwindow

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

// A private disk projection keeps replayed conversation history out of the RAM tail.
// The host remains authoritative; each new window rebuilds this disposable cache.
func (w *Window) openHistory() error {
	dir, err := os.MkdirTemp("", "agent-room-history-")
	if err != nil {
		return err
	}
	w.historyDir = dir
	db, err := sql.Open("sqlite", filepath.Join(dir, "history.db"))
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	db.SetMaxOpenConns(1)
	w.history = db
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; CREATE TABLE answers (seq INTEGER PRIMARY KEY, body TEXT NOT NULL, client_id TEXT, turn TEXT, role TEXT);
 CREATE INDEX answers_client ON answers(client_id); CREATE INDEX answers_turn ON answers(turn);`)
	return err
}
func (w *Window) saveAnswer(a Answer) (bool, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	result, err := w.history.Exec(`INSERT OR IGNORE INTO answers(seq,body,client_id,turn,role) VALUES(?,?,?,?,?)`, a.Seq, string(b), a.ClientID, a.Turn, a.Role)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
func (w *Window) historyPage(before uint64) ([]Answer, bool, error) {
	if before == 0 {
		before = 1<<63 - 1
	}
	rows, err := w.history.Query(`SELECT body FROM answers WHERE seq < ? ORDER BY seq DESC LIMIT 51`, before)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var result []Answer
	totalBytes := 0
	byteLimited := false
	for rows.Next() {
		var b string
		var a Answer
		if err := rows.Scan(&b); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal([]byte(b), &a); err != nil {
			return nil, false, err
		}
		if len(result) > 0 && totalBytes+len(a.Text) > maxTextBytes {
			byteLimited = true
			break
		}
		totalBytes += len(a.Text)
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(result) > 50 || byteLimited
	if len(result) > 50 {
		result = result[:50]
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, more, nil
}
func (w *Window) serveHistory(out http.ResponseWriter, r *http.Request) {
	before, err := strconv.ParseUint(r.URL.Query().Get("before"), 10, 63)
	if err != nil || before == 0 {
		http.Error(out, "invalid history cursor", 400)
		return
	}
	w.mu.Lock()
	answers, more, err := w.historyPage(before)
	for i := range answers {
		if answers[i].Role == "user" {
			if name := w.names[answers[i].UID]; name != "" {
				answers[i].Author = name
			} else {
				answers[i].Author = fmt.Sprintf("成员 %d", answers[i].UID)
			}
		}
	}
	roomID := w.room
	w.mu.Unlock()
	if err != nil {
		http.Error(out, "cannot read history", 500)
		return
	}
	out.Header().Set("Content-Type", "application/json")
	json.NewEncoder(out).Encode(struct {
		Answers []Answer `json:"answers"`
		HasMore bool     `json:"hasMore"`
		Room    string   `json:"room"`
	}{answers, more, roomID})
}

func (w *Window) searchHistory(out http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) > 256 || q == "" {
		http.Error(out, "请输入 1–256 字节关键词", 400)
		return
	}
	before := uint64(1<<63 - 1)
	if value := r.URL.Query().Get("before"); value != "" {
		var err error
		before, err = strconv.ParseUint(value, 10, 63)
		if err != nil {
			http.Error(out, "invalid cursor", 400)
			return
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	rows, err := w.history.Query(`SELECT body FROM answers WHERE seq < ? AND instr(lower(json_extract(body,'$.text')),lower(?))>0 ORDER BY seq DESC LIMIT 51`, before, q)
	if err != nil {
		http.Error(out, "搜索失败", 500)
		return
	}
	defer rows.Close()
	var answers []Answer
	for rows.Next() {
		var body string
		var a Answer
		if rows.Scan(&body) != nil || json.Unmarshal([]byte(body), &a) != nil {
			http.Error(out, "搜索失败", 500)
			return
		}
		answers = append(answers, a)
	}
	more := len(answers) > 50
	if more {
		answers = answers[:50]
	}
	out.Header().Set("Content-Type", "application/json")
	json.NewEncoder(out).Encode(struct {
		Answers []Answer `json:"answers"`
		More    bool     `json:"more"`
	}{answers, more})
}
