package answerwindow

import (
	"agent_romm/internal/network"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type UploadFunc func(context.Context, string, int64, io.ReadSeeker) (network.Upload, error)

func (w *Window) EnableUploads(upload UploadFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.upload = upload
}
func (w *Window) postUpload(out http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(out, "same-origin request required", 403)
		return
	}
	w.mu.Lock()
	upload := w.upload
	connected := w.connected
	w.mu.Unlock()
	if upload == nil || !connected {
		http.Error(out, "上传不可用，请先连接 host", 503)
		return
	}
	r.Body = http.MaxBytesReader(out, r.Body, network.MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
		http.Error(out, "文件最大 20 MiB", 400)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		http.Error(out, "每次请选择一个文件", 400)
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		http.Error(out, "missing file", 400)
		return
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	file, err := upload(ctx, header.Filename, header.Size, f)
	if err != nil {
		http.Error(out, err.Error(), 502)
		return
	}
	out.Header().Set("Content-Type", "application/json")
	json.NewEncoder(out).Encode(file)
}
