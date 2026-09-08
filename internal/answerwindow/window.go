// Package answerwindow serves a shared chat viewer on the local machine.
package answerwindow

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"agent_romm/internal/buildinfo"
	"agent_romm/internal/client"
	"agent_romm/internal/room"
)

//go:embed index.html
var page []byte

//go:embed app.js
var script []byte

//go:embed group.mjs
var grouping []byte

//go:embed markdown.mjs
var markdown []byte

//go:embed vendor/marked.mjs
var marked []byte

//go:embed tasks.mjs
var taskScript []byte

//go:embed style.css
var style []byte

const maxAnswers = 100
const maxTextBytes = 8 << 20

type Answer struct {
	Attachments []Attachment `json:"attachments,omitempty"`
	TaskStatus  string       `json:"taskStatus,omitempty"`
	Owner       *room.UID    `json:"owner,omitempty"`
	Ack         string       `json:"ack,omitempty"`
	Turn        string       `json:"turn,omitempty"`
	Seq         uint64       `json:"seq"`
	Text        string       `json:"text"`
	Role        string       `json:"role"`
	Author      string       `json:"author"`
	UID         room.UID     `json:"uid"`
	Kind        string       `json:"kind"`
	ClientID    string       `json:"clientId"`
	Time        string       `json:"time"`
}
type Window struct {
	queue        []room.QueuedMessage
	userRole     string
	askReady     bool
	query        QueryFunc
	tasks        TaskFunc
	draftDir     string
	download     DownloadFunc
	activeTurn   string
	upload       UploadFunc
	history      *sql.DB
	historyDir   string
	connected    bool
	host         string
	session      string
	viewer       string
	turnOwners   map[string]room.UID
	turnStates   map[string]string
	turnMessages map[string]string
	messageTurns map[string]string
	turnOrder    []string
	mu           sync.Mutex
	names        map[room.UID]string
	submissions  chan client.Submission
	answers      []Answer
	bytes        int
	revision     uint64
	dropped      bool
	status       string
	room         string
	server       *http.Server
	listener     net.Listener
	path         string
}

func Start() (*Window, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	cfg, _ := os.UserConfigDir()
	w := &Window{draftDir: filepath.Join(cfg, "agent_room", "drafts"), listener: l, path: "/" + hex.EncodeToString(token[:]) + "/", status: "正在连接", names: make(map[room.UID]string)}
	if err := w.openHistory(); err != nil {
		l.Close()
		if w.history != nil {
			w.history.Close()
		}
		os.RemoveAll(w.historyDir)
		return nil, err
	}
	w.server = &http.Server{Handler: http.HandlerFunc(w.serve), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, ReadTimeout: 110 * time.Second, WriteTimeout: 110 * time.Second}
	go w.server.Serve(l)
	return w, nil
}
func (w *Window) URL() string { return "http://" + w.listener.Addr().String() + w.path }
func (w *Window) Close() error {
	err := w.server.Close()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.history.Close()
	os.RemoveAll(w.historyDir)
	return err
}
func (w *Window) Room(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.room == id {
		return
	}
	w.queue = nil
	w.activeTurn = ""
	w.room = id
	if _, err := w.history.Exec("DELETE FROM answers"); err != nil {
		w.status = "历史缓存重置失败"
	}
	w.turnOwners = make(map[string]room.UID)
	w.turnStates = make(map[string]string)
	w.turnMessages = make(map[string]string)
	w.messageTurns = make(map[string]string)
	w.turnOrder = nil
	w.names = make(map[room.UID]string)
	w.answers = nil
	w.bytes = 0
	w.dropped = false
	w.revision++
}
func (w *Window) Connection(connected bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.connected = connected
	if connected {
		w.status = "已连接"
	} else {
		w.status = "与 host 的连接中断 · 正在重连"
	}
	w.revision++
}
func (w *Window) Status(status string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
	w.revision++
}
func (w *Window) Add(e room.DurableEvent, text string) error {
	var body struct {
		Turn string `json:"turn_id"`
	}
	if len(e.Payload) > 0 {
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			return err
		}
	}
	return w.add(Answer{Turn: body.Turn, Seq: uint64(e.Seq), Text: text, Role: "assistant", Author: "模型", Time: e.CreatedAt.Format(time.RFC3339)})
}
func (w *Window) add(message Answer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if message.Role == "user" {
		uid := message.UID
		message.Owner = &uid
	} else if uid, ok := w.turnOwners[message.Turn]; ok {
		message.Owner = &uid
	}
	inserted, err := w.saveAnswer(message)
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	w.answers = append(w.answers, message)
	w.bytes += len(message.Text)
	for len(w.answers) > 1 && (len(w.answers) > maxAnswers || w.bytes > maxTextBytes) {
		w.bytes -= len(w.answers[0].Text)
		w.answers = w.answers[1:]
		w.dropped = true
	}
	w.status = "已连接 · 等待下一条完整回答"
	w.revision++
	return nil
}
func (w *Window) serve(out http.ResponseWriter, r *http.Request) {
	// Bind to loopback and require the exact host and unguessable path. No CORS,
	// remote assets or rendering model-provided HTML are permitted.
	if r.Host != w.listener.Addr().String() {
		http.Error(out, "invalid host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		http.Error(out, "invalid origin", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && !(r.Method == http.MethodPost && (r.URL.Path == w.path+"tasks" || r.URL.Path == w.path+"questions" || r.URL.Path == w.path+"draft" || r.URL.Path == w.path+"submit" || r.URL.Path == w.path+"cancel" || r.URL.Path == w.path+"upload")) {
		out.Header().Set("Allow", "GET")
		http.Error(out, "read-only", http.StatusMethodNotAllowed)
		return
	}
	out.Header().Set("Cache-Control", "no-store")
	out.Header().Set("X-Content-Type-Options", "nosniff")
	out.Header().Set("Referrer-Policy", "no-referrer")
	out.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' blob:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	switch r.URL.Path {
	case w.path + "tasks":
		w.serveTasks(out, r)
	case w.path + "questions":
		w.serveQuestions(out, r)
	case w.path + "draft":
		w.serveDraft(out, r)
	case w.path + "file":
		w.serveFile(out, r)
	case w.path + "upload":
		if r.Method != http.MethodPost {
			http.Error(out, "POST required", 405)
			return
		}
		w.postUpload(out, r)
	case w.path + "submit", w.path + "cancel":
		if r.Method != http.MethodPost {
			http.Error(out, "POST required", 405)
			return
		}
		w.post(out, r)
	case w.path:
		out.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = out.Write(page)
	case w.path + "tasks.mjs":
		out.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = out.Write(taskScript)
	case w.path + "group.mjs":
		out.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = out.Write(grouping)
	case w.path + "markdown.mjs":
		out.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = out.Write(markdown)
	case w.path + "vendor/marked.mjs":
		out.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = out.Write(marked)
	case w.path + "app.js":
		out.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = out.Write(script)
	case w.path + "style.css":
		out.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = out.Write(style)
	case w.path + "search":
		w.searchHistory(out, r)
	case w.path + "history":
		w.serveHistory(out, r)
	case w.path + "answers":
		w.mu.Lock()
		page, more, err := w.historyPage(0)
		if err != nil {
			w.mu.Unlock()
			http.Error(out, "cannot read history", 500)
			return
		}
		state := struct {
			Queue         []room.QueuedMessage `json:"queue"`
			UserRole      string               `json:"userRole"`
			AskReady      bool                 `json:"askReady"`
			ActiveTurn    string               `json:"activeTurn"`
			Revision      uint64               `json:"revision"`
			Answers       []Answer             `json:"answers"`
			Dropped       bool                 `json:"dropped"`
			Status        string               `json:"status"`
			Room          string               `json:"room"`
			Host          string               `json:"host"`
			Session       string               `json:"session"`
			Viewer        string               `json:"viewer"`
			Members       []room.Member        `json:"members"`
			ClientVersion string               `json:"clientVersion"`
			Connected     bool                 `json:"connected"`
		}{w.queue, w.userRole, w.askReady, w.activeTurn, w.revision, page, more, w.status, w.room, w.host, w.session, w.viewer, nil, buildinfo.Version, w.connected}
		for uid, name := range w.names {
			state.Members = append(state.Members, room.Member{UID: uid, Name: name})
		}
		sort.Slice(state.Members, func(i, j int) bool { return state.Members[i].UID < state.Members[j].UID })
		for i := range state.Answers {
			if status := w.turnStates[state.Answers[i].Turn]; status != "" {
				state.Answers[i].TaskStatus = status
			}
			if state.Answers[i].Role == "user" {
				name := w.names[state.Answers[i].UID]
				if name == "" {
					name = fmt.Sprintf("成员 %d", state.Answers[i].UID)
				}
				state.Answers[i].Author = name
			}
		}
		w.mu.Unlock()
		out.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.URL.Query().Get("revision") == fmt.Sprint(state.Revision) {
			out.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(out).Encode(state)
	default:
		http.NotFound(out, r)
	}
}

func (w *Window) ActiveTurn(turn string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.activeTurn != turn {
		w.activeTurn = turn
		w.revision++
	}
}

func (w *Window) Queue(snapshot room.Snapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queue = snapshot.Queue
	w.revision++
}
