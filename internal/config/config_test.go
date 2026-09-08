package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayoutRejectsProjectAndSymlink(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"relative", root, filepath.Join(root, "state")} {
		if _, e := NewLayout(p, root); e == nil {
			t.Fatal(p)
		}
	}
	other := t.TempDir()
	link := filepath.Join(other, "link")
	if e := os.Symlink(root, link); e != nil {
		t.Fatal(e)
	}
	if _, e := NewLayout(filepath.Join(link, "state"), other); e == nil {
		t.Fatal("symlink accepted")
	}
}
