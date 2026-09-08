package answerwindow

import (
	"agent_romm/internal/network"
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

type QueryFunc func(context.Context, network.QueryRequest) (network.QueryReply, error)

func (w *Window) EnableQuestions(fn QueryFunc) { w.mu.Lock(); defer w.mu.Unlock(); w.query = fn }
func (w *Window) RefreshRole(ctx context.Context) {
	w.mu.Lock()
	fn := w.query
	w.mu.Unlock()
	if fn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reply, err := fn(ctx, network.QueryRequest{Action: "info"})
	if err != nil {
		return
	}
	w.mu.Lock()
	w.userRole = reply.Role
	w.askReady = reply.Ready
	w.revision++
	w.mu.Unlock()
}
func (w *Window) serveQuestions(out http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	fn, role := w.query, w.userRole
	w.mu.Unlock()
	if fn == nil || role == "visitor" {
		http.Error(out, "独立问答不可用", 403)
		return
	}
	req := network.QueryRequest{Action: "list"}
	if r.Method == "POST" {
		if r.Header.Get("Origin") != "http://"+r.Host || role != "asker" {
			http.Error(out, "仅询问者可以提问", 403)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(out, r.Body, 8192))
		decoder.DisallowUnknownFields()
		var body struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(out, "invalid question", 400)
			return
		}
		req.Action = "ask"
		req.ID = body.ID
		req.Text = body.Text
	} else {
		req.Before, _ = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		uid, _ := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
		req.UID = room.UID(uid)
	}
	reply, err := fn(r.Context(), req)
	if err != nil {
		http.Error(out, err.Error(), 400)
		return
	}
	out.Header().Set("Content-Type", "application/json")
	json.NewEncoder(out).Encode(reply)
}
