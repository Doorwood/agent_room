package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const MaxUploadBytes int64 = 20 << 20
const maxRoomUploadBytes int64 = 1 << 30

type Upload struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}
type uploadRequest struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type uploadReply struct {
	File  Upload `json:"file"`
	Error string `json:"error,omitempty"`
}

func uploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r) {
			return r
		}
		return '_'
	}, name)
	if len(name) > 180 || name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

// Upload transfers bytes over the same pinned TLS and membership gate as room streams.
func (l Launcher) Upload(ctx context.Context, name string, size int64, source io.ReadSeeker) (Upload, error) {
	if size < 0 || size > MaxUploadBytes {
		return Upload{}, errors.New("文件最大 20 MiB")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(source, MaxUploadBytes+1))
	if err != nil {
		return Upload{}, err
	}
	if n != size {
		return Upload{}, errors.New("file size mismatch")
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return Upload{}, err
	}
	c, r, err := l.dialOperation(ctx, "upload")
	if err != nil {
		return Upload{}, fmt.Errorf("无法上传，请确认 host 已升级并支持附件：%w", err)
	}
	defer c.Close()
	if r.State != "approved" {
		return Upload{}, fmt.Errorf("membership %s", r.State)
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	if err = writeJSON(c, uploadRequest{uploadName(name), size, hex.EncodeToString(hash.Sum(nil))}); err != nil {
		return Upload{}, err
	}
	if _, err = io.CopyN(c, source, size); err != nil {
		return Upload{}, err
	}
	var result uploadReply
	if err = readJSON(c, &result); err != nil {
		return Upload{}, err
	}
	if result.Error != "" {
		return Upload{}, errors.New(result.Error)
	}
	if result.File.Path == "" || result.File.Size != size {
		return Upload{}, errors.New("invalid upload confirmation")
	}
	return result.File, nil
}
func (s *Server) receiveUpload(c net.Conn, token string) {
	c.SetDeadline(time.Now().Add(60 * time.Second))
	var req uploadRequest
	if err := readJSON(c, &req); err != nil {
		return
	}
	digest, err := hex.DecodeString(req.SHA256)
	if req.Size < 0 || req.Size > MaxUploadBytes || err != nil || len(digest) != 32 || hex.EncodeToString(digest) != req.SHA256 || req.Name != uploadName(req.Name) {
		writeJSON(c, uploadReply{Error: "invalid upload"})
		return
	}
	// Serialize quota checks and commits. Revocation closes the connection even while queued.
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	dir, err := filepath.Abs(filepath.Join(s.privateDir, "uploads"))
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeJSON(c, uploadReply{Error: "cannot create upload directory"})
		return
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		writeJSON(c, uploadReply{Error: "invalid upload directory"})
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var used int64
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil {
			used += info.Size()
		}
	}
	if used+req.Size > maxRoomUploadBytes {
		writeJSON(c, uploadReply{Error: "Room 附件空间已达到 1 GiB，请联系 host 清理"})
		return
	}
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	if _, err = io.CopyN(io.MultiWriter(f, hash), c, req.Size); err != nil {
		return
	}
	if hex.EncodeToString(hash.Sum(nil)) != req.SHA256 {
		writeJSON(c, uploadReply{Error: "file checksum mismatch"})
		return
	}
	if err = f.Sync(); err != nil {
		return
	}
	if err = f.Close(); err != nil {
		return
	}
	target := filepath.Join(dir, req.SHA256+"-"+req.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.store.AuthenticateJoin(s.ctx, s.room, token); err != nil {
		writeJSON(c, uploadReply{Error: "membership no longer approved"})
		return
	}
	if err = os.Rename(f.Name(), target); err != nil {
		return
	}
	writeJSON(c, uploadReply{File: Upload{Name: req.Name, Path: target, Size: req.Size}})
}
