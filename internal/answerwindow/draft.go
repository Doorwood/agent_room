package answerwindow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

func (w *Window) DraftDirectory(dir string) { w.mu.Lock(); defer w.mu.Unlock(); w.draftDir = dir }
func (w *Window) draftPath() string {
	sum := sha256.Sum256([]byte(w.host + "\n" + w.session + "\n" + w.viewer))
	return filepath.Join(w.draftDir, hex.EncodeToString(sum[:])+".json")
}
func (w *Window) serveDraft(out http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.host == "" || w.session == "" || w.viewer == "" {
		http.Error(out, "identity unavailable", 409)
		return
	}
	target := w.draftPath()
	out.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" {
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			out.Write([]byte("{}"))
			return
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 96<<10 {
			http.Error(out, "草稿读取失败", 500)
			return
		}
		b, err := os.ReadFile(target)
		if os.IsNotExist(err) {
			out.Write([]byte("{}"))
			return
		}
		if err != nil {
			http.Error(out, "草稿读取失败", 500)
			return
		}
		out.Write(b)
		return
	}
	if r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(out, "same-origin required", 403)
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(out, r.Body, 96<<10))
	if err != nil || !json.Valid(b) {
		http.Error(out, "invalid draft", 400)
		return
	}
	if err = os.MkdirAll(w.draftDir, 0700); err != nil {
		http.Error(out, "草稿保存失败", 500)
		return
	}
	info, err := os.Lstat(w.draftDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		http.Error(out, "草稿目录权限必须为 0700", 500)
		return
	}
	f, err := os.CreateTemp(w.draftDir, ".draft-*")
	if err != nil {
		http.Error(out, "草稿保存失败", 500)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(b); err == nil {
		err = f.Close()
	}
	if err == nil {
		err = os.Rename(f.Name(), target)
	}
	if err != nil {
		http.Error(out, "草稿保存失败", 500)
		return
	}
	out.Write([]byte(`{"saved":true}`))
}
