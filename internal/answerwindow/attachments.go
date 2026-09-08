package answerwindow

import (
	"agent_romm/internal/network"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

type DownloadFunc func(context.Context, string, io.Writer) (network.Upload, error)

func (w *Window) EnableDownloads(fn DownloadFunc) { w.mu.Lock(); defer w.mu.Unlock(); w.download = fn }
func (w *Window) serveFile(out http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	fn := w.download
	w.mu.Unlock()
	if fn == nil {
		http.Error(out, "附件读取不可用", 503)
		return
	}
	var data bytes.Buffer
	file, err := fn(r.Context(), r.URL.Query().Get("id"), &data)
	if err != nil {
		http.Error(out, "附件读取失败，请检查连接或联系 host", 404)
		return
	}
	media := http.DetectContentType(data.Bytes())
	inline := r.URL.Query().Get("preview") == "1" && (media == "image/png" || media == "image/jpeg" || media == "image/gif" || media == "image/webp")
	disposition := "attachment"
	if inline {
		disposition = "inline"
	} else {
		media = "application/octet-stream"
	}
	out.Header().Set("Content-Type", media)
	out.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": file.Name}))
	out.Write(data.Bytes())
}

type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func splitAttachments(text string) (string, []Attachment) {
	marker := "附件已上传到 host，请根据需要读取文件；图片可用图片查看工具打开：\n"
	at := strings.LastIndex(text, marker)
	if at < 0 {
		return text, nil
	}
	var files []Attachment
	for _, line := range strings.Split(strings.TrimSpace(text[at+len(marker):]), "\n") {
		var file network.Upload
		if json.Unmarshal([]byte(line), &file) != nil || file.Name == "" || file.Path == "" {
			return text, nil
		}
		id := filepath.Base(file.Path)
		if len(id) < 66 || len(files) >= 10 {
			return text, nil
		}
		files = append(files, Attachment{ID: id, Name: file.Name})
	}
	return strings.TrimSpace(text[:at]), files
}
