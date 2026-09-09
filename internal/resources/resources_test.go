package resources

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResourceAllowlistAndProjectBoundary(t *testing.T) {
	for _, r := range []Request{
		{Action: "shell", Target: "cat ~/.ssh/id_rsa"},
		{Action: "github.repo", Target: "https://evil.test/x/y"},
		{Action: "github.repo", Target: "a/../b"},
		{Action: "github.issue", Target: "a/b#1 --hostname evil"},
		{Action: "feishu.document", Target: "--help"},
		{Action: "project.file", Target: "../secret"},
		{Action: "project.file", Target: ".env"},
		{Action: "project.file", Target: "nested/.git/config"},
	} {
		if r.Validate() == nil {
			t.Fatal("accepted", r)
		}
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.txt")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(root, "link"))
	os.WriteFile(filepath.Join(root, "README.md"), []byte("public project text"), 0600)
	e := Executor{Root: root}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := e.Run(ctx, Request{Action: "project.file", Target: "link"}); err == nil {
		t.Fatal("followed outside symlink")
	}
	got, err := e.Run(ctx, Request{Action: "project.file", Target: "README.md"})
	if err != nil || got != "public project text" {
		t.Fatal(got, err)
	}
	os.WriteFile(filepath.Join(root, "large"), make([]byte, MaxResult+1), 0600)
	if _, err = e.Run(ctx, Request{Action: "project.file", Target: "large"}); err == nil {
		t.Fatal("large output accepted")
	}
}
