package gitview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiteralNamesAndEnvironment(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", root).Run(); err != nil {
		t.Fatal(err)
	}
	name := "space ; `touch PWNED` $(touch PWNED)"
	if err := os.WriteFile(filepath.Join(root, name), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", "/missing")
	out, err := (View{Root: root}).Status(context.Background())
	if err != nil || !strings.Contains(string(out), "PWNED") {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(root, "PWNED")); !os.IsNotExist(err) {
		t.Fatal("executed filename")
	}
}

func TestDiffBoundsAndDisablesHelpers(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", out, e)
		}
	}
	git("init")
	file := filepath.Join(root, "tracked")
	os.WriteFile(file, []byte("before\n"), 0600)
	os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("tracked diff=custom\n"), 0600)
	git("add", ".")
	marker := filepath.Join(root, "executed")
	git("config", "diff.custom.textconv", "touch "+marker)
	git("config", "diff.custom.binary", "false")
	git("config", "diff.external", "touch "+marker)
	git("config", "core.fsmonitor", "touch "+marker)
	os.WriteFile(file, []byte("after\n"), 0600)
	if _, e := (View{Root: root}).Diff(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := (View{Root: root}).Status(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("executed Git helper")
	}
	os.WriteFile(file, []byte(strings.Repeat("abcdef\n", 800000)), 0600)
	if b, e := (View{Root: root}).Diff(context.Background()); !errors.Is(e, ErrOutputLimit) {
		t.Fatalf("unbounded: %v output=%d %.200s", e, len(b), b)
	}
}
func TestCancelledGitReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, e := (View{Root: t.TempDir()}).Status(ctx); e == nil {
		t.Fatal("ignored cancellation")
	}
	if time.Since(start) > time.Second {
		t.Fatal("did not reap promptly")
	}
}
func TestBoundedWriter(t *testing.T) {
	cancelled := false
	w := boundedWriter{limit: 3, cancel: func() { cancelled = true }}
	_, err := w.Write([]byte("1234"))
	if err == nil || !cancelled || w.Len() != 3 {
		t.Fatal("not bounded")
	}
}

func TestProjectionRejectsConfiguredFiltersBeforeHelpersOrDescendantsStart(t *testing.T) {
	for _, method := range []string{"status", "diff"} {
		for _, kind := range []string{"clean", "process"} {
			for _, included := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/included=%v", method, kind, included), func(t *testing.T) {
					root := t.TempDir()
					git := func(args ...string) {
						t.Helper()
						cmd := exec.Command("git", args...)
						cmd.Dir = root
						if out, e := cmd.CombinedOutput(); e != nil {
							t.Fatalf("git: %s %v", out, e)
						}
					}
					git("init")
					if e := os.WriteFile(filepath.Join(root, "tracked"), []byte("before\n"), 0600); e != nil {
						t.Fatal(e)
					}
					git("add", "tracked")
					if e := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("tracked filter=custom\n"), 0600); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(filepath.Join(root, "tracked"), []byte("after\n"), 0600); e != nil {
						t.Fatal(e)
					}
					// If launched, this helper creates immediate and delayed evidence. Its
					// background child would survive a direct parent-only cancellation.
					helper := "touch helper-started; (sleep 0.15; touch descendant-started) & cat"
					if included {
						path := filepath.Join(root, "included.conf")
						git("config", "--file", path, "filter.custom."+kind, helper)
						git("config", "include.path", path)
					} else {
						git("config", "filter.custom."+kind, helper)
					}
					var err error
					if method == "status" {
						_, err = (View{Root: root}).Status(context.Background())
					} else {
						_, err = (View{Root: root}).Diff(context.Background())
					}
					if !errors.Is(err, ErrUnsafeFilter) {
						t.Fatalf("filter not rejected: %v", err)
					}
					time.Sleep(200 * time.Millisecond)
					for _, name := range []string{"helper-started", "descendant-started"} {
						if _, e := os.Stat(filepath.Join(root, name)); !os.IsNotExist(e) {
							t.Fatalf("started %s", name)
						}
					}
				})
			}
		}
	}
}

func TestProjectionSkipsSubmoduleWorktreeHelpersButShowsGitlinkChanges(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %s %v", out, e)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init")
	if e := os.Mkdir(sub, 0700); e != nil {
		t.Fatal(e)
	}
	git(sub, "init")
	file := filepath.Join(sub, "tracked")
	if e := os.WriteFile(file, []byte("before\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git(sub, "add", ".")
	git(sub, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "first")
	oid := git(sub, "rev-parse", "HEAD")
	git(root, "update-index", "--add", "--cacheinfo", "160000", oid, "sub")
	git(root, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "initial gitlink")
	if e := os.WriteFile(file, []byte("next commit\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git(sub, "add", ".")
	git(sub, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "second")
	if e := os.WriteFile(filepath.Join(sub, ".gitattributes"), []byte("tracked filter=custom\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git(sub, "config", "filter.custom.clean", "touch sub-helper-started; cat")
	if e := os.WriteFile(file, []byte("dirty\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git(root, "config", "status.submoduleSummary", "true")
	git(root, "config", "diff.submodule", "log")
	status, e := (View{Root: root}).Status(context.Background())
	if e != nil || !strings.Contains(string(status), "sub") {
		t.Fatalf("gitlink status missing: %s %v", status, e)
	}
	diff, e := (View{Root: root}).Diff(context.Background())
	if e != nil || !strings.Contains(string(diff), "Subproject commit") {
		t.Fatalf("gitlink diff missing: %s %v", diff, e)
	}
	if _, e := os.Stat(filepath.Join(sub, "sub-helper-started")); !os.IsNotExist(e) {
		t.Fatal("submodule helper executed")
	}
}
