package network

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

func validFileID(id string) bool {
	if len(id) < 66 || len(id) > 245 || id[64] != '-' || filepath.Base(id) != id {
		return false
	}
	b, err := hex.DecodeString(id[:64])
	return err == nil && len(b) == 32 && id[65:] == uploadName(id[65:])
}
func (s *Server) sendFile(c net.Conn) {
	c.SetDeadline(time.Now().Add(60 * time.Second))
	var req struct {
		ID string `json:"id"`
	}
	if readJSON(c, &req) != nil || !validFileID(req.ID) {
		return
	}
	path := filepath.Join(s.privateDir, "uploads", req.ID)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxUploadBytes {
		writeJSON(c, uploadReply{Error: "附件不存在或已清理"})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if writeJSON(c, uploadReply{File: Upload{Name: req.ID[65:], Size: info.Size()}}) != nil {
		return
	}
	io.CopyN(c, f, info.Size())
}
func (l Launcher) Download(ctx context.Context, id string, dest io.Writer) (Upload, error) {
	if !validFileID(id) {
		return Upload{}, errors.New("invalid attachment ID")
	}
	c, r, err := l.dialOperation(ctx, "download")
	if err != nil {
		return Upload{}, err
	}
	defer c.Close()
	if r.State != "approved" {
		return Upload{}, errors.New("membership not approved")
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	if err = writeJSON(c, struct {
		ID string `json:"id"`
	}{id}); err != nil {
		return Upload{}, err
	}
	var reply uploadReply
	if err = readJSON(c, &reply); err != nil {
		return Upload{}, err
	}
	if reply.Error != "" {
		return Upload{}, errors.New(reply.Error)
	}
	if reply.File.Size < 0 || reply.File.Size > MaxUploadBytes {
		return Upload{}, errors.New("invalid file size")
	}
	_, err = io.CopyN(dest, c, reply.File.Size)
	return reply.File, err
}
