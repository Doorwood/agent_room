package answerwindow

import (
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

type TaskFunc func(context.Context, room.TaskRequest) (room.TaskReply, error)

func (w *Window) EnableTasks(fn TaskFunc) { w.mu.Lock(); defer w.mu.Unlock(); w.tasks = fn }
func (w *Window) serveTasks(out http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	fn, role := w.tasks, w.userRole
	w.mu.Unlock()
	if fn == nil {
		http.Error(out, "项目任务不可用，请更新 Host 和客户端", 503)
		return
	}
	req := room.TaskRequest{Action: "list"}
	if r.Method == http.MethodPost {
		if r.Header.Get("Origin") != "http://"+r.Host || role != "roommate" {
			http.Error(out, "只有协作成员可以修改任务", 403)
			return
		}
		dec := json.NewDecoder(http.MaxBytesReader(out, r.Body, 8192))
		dec.DisallowUnknownFields()
		if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
			http.Error(out, "invalid task request", 400)
			return
		}
		if req.Action != "convert" && req.Action != "complete" && req.Action != "reopen" {
			http.Error(out, "invalid task action", 400)
			return
		}
	} else {
		if id := r.URL.Query().Get("id"); id != "" {
			req.Action = "get"
			req.TaskID, _ = strconv.ParseInt(id, 10, 64)
		}
		req.Before, _ = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	}
	reply, err := fn(r.Context(), req)
	if err != nil {
		http.Error(out, err.Error(), 400)
		return
	}
	out.Header().Set("Content-Type", "application/json")
	json.NewEncoder(out).Encode(reply)
}
