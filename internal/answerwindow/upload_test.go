package answerwindow

import (
	"agent_romm/internal/network"
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

func TestUploadEndpointOriginConnectionAndBytes(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	calls := 0
	w.EnableUploads(func(ctx context.Context, name string, size int64, r io.ReadSeeker) (network.Upload, error) {
		calls++
		b, err := io.ReadAll(r)
		if err != nil || string(b) != "file content" || size != 12 {
			t.Fatal("incorrect file bytes", err)
		}
		return network.Upload{Name: name, Path: "/host/upload/notes.txt", Size: size}, nil
	})
	send := func(origin string) int {
		var body bytes.Buffer
		m := multipart.NewWriter(&body)
		f, _ := m.CreateFormFile("file", "notes.txt")
		f.Write([]byte("file content"))
		m.Close()
		req := httptest.NewRequest("POST", w.URL()+"upload", &body)
		req.Header.Set("Content-Type", m.FormDataContentType())
		req.Header.Set("Origin", origin)
		out := httptest.NewRecorder()
		w.serve(out, req)
		return out.Code
	}
	origin := "http://" + w.listener.Addr().String()
	if code := send(origin); code != 503 {
		t.Fatal(code)
	}
	w.Connection(true)
	if code := send("https://evil.example"); code != 403 {
		t.Fatal(code)
	}
	if code := send(""); code != 403 {
		t.Fatal(code)
	}
	if calls != 0 {
		t.Fatal("unauthorized upload callback")
	}
	if code := send(origin); code != 200 || calls != 1 {
		t.Fatal(code, calls)
	}
}
