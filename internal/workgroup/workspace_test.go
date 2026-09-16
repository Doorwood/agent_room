package workgroup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent_romm/internal/room"
)

func workspaceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, e := exec.Command("git", "init", dir).CombinedOutput(); e != nil {
		t.Fatal(string(out), e)
	}
	return dir
}
func writeFixture(t *testing.T, dir, p, text string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(dir, p), []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestWorkspaceSnapshotProtectsSourceAndRejectsUnsafePaths(t *testing.T) {
	dir := workspaceRepo(t)
	writeFixture(t, dir, "code.go", "base")
	writeFixture(t, dir, ".env", "secret")
	writeFixture(t, dir, ".gitignore", "ignored\n")
	writeFixture(t, dir, "ignored", "private")
	snap, e := SnapshotProject(context.Background(), dir, true)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range snap.Files {
		if f.Path == ".env" || f.Path == "ignored" || strings.HasPrefix(f.Path, ".git/") {
			t.Fatal(f.Path)
		}
	}
	copyDir, e := MaterializeWorkspace(t.TempDir(), snap)
	if e != nil {
		t.Fatal(e)
	}
	writeFixture(t, copyDir, "code.go", "changed")
	original, _ := os.ReadFile(filepath.Join(dir, "code.go"))
	if string(original) != "base" {
		t.Fatal("source changed")
	}
	for _, p := range []string{"../outside", "/absolute", "a/../../escape", ".git/config", ".env", "a\\b"} {
		if _, e := MaterializeWorkspace(t.TempDir(), WorkspaceSnapshot{Files: []WorkspaceFile{{Path: p}}}); e == nil {
			t.Fatal("unsafe path", p)
		}
	}
	os.Symlink(filepath.Join(dir, "code.go"), filepath.Join(dir, "link"))
	if _, e := SnapshotProject(context.Background(), dir, true); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestWorkspaceConflictsAndDisjointChanges(t *testing.T) {
	base := WorkspaceSnapshot{Files: []WorkspaceFile{{Path: "a", Data: []byte("old")}, {Path: "b", Data: []byte("old")}}}
	one := WorkspaceSnapshot{Files: []WorkspaceFile{{Path: "a", Data: []byte("one")}, {Path: "b", Data: []byte("old")}}}
	two := WorkspaceSnapshot{Files: []WorkspaceFile{{Path: "a", Data: []byte("old")}, {Path: "b", Data: []byte("two")}}}
	merged, e := ApplyWorkspaceChanges(base, append(WorkspaceDiff(base, one), WorkspaceDiff(base, two)...))
	if e != nil {
		t.Fatal(e)
	}
	if string(merged.Files[0].Data) != "one" || string(merged.Files[1].Data) != "two" {
		t.Fatal(merged)
	}
	if _, e := ApplyWorkspaceChanges(one, WorkspaceDiff(base, two)); e != nil {
		t.Fatal(e)
	}
	if _, e := ApplyWorkspaceChanges(one, WorkspaceDiff(base, one)); e == nil {
		t.Fatal("stale baseline overwrote newer file")
	}
	if _, e := ApplyWorkspaceChanges(base, []WorkspaceChange{{File: WorkspaceFile{Path: "a"}, Delete: true, Before: "wrong"}}); e == nil {
		t.Fatal("stale delete allowed")
	}
}
func TestRemoteHostWorkspaceIsolationAndDependencyHandoff(t *testing.T) {
	root := workspaceRepo(t)
	writeFixture(t, root, "code.txt", "original")
	broker := NewRemoteBroker()
	broker.root = root
	broker.workspaceDir = t.TempDir()
	inv, e := broker.Handle(1, WorkerRequest{Action: "invite", ID: strings.Repeat("a", 32), Provider: "cursor", Mode: "work", WorkspaceScope: "host"})
	if _, err := broker.Handle(1, WorkerRequest{Action: "health", ID: inv.ID, Healthy: true}); err != nil {
		t.Fatal(err)
	}
	if e != nil {
		t.Fatal(e)
	}
	member := broker.Members()[0]
	run := func(prior []Result, change bool) Result {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out := make(chan Result, 1)
		errs := make(chan error, 1)
		go func() {
			r, e := broker.Run(ctx, member, Assignment{ID: strings.Repeat("b", 32), HumanPrompt: "work", Prior: prior})
			if e != nil {
				errs <- e
			} else {
				out <- r
			}
		}()
		var job WorkerReply
		for i := 0; i < 100; i++ {
			job, e = broker.Handle(1, WorkerRequest{Action: "poll", ID: inv.ID})
			if e != nil {
				t.Fatal(e)
			}
			if job.Assignment != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if job.Assignment == nil || job.Assignment.Workspace == nil {
			t.Fatal("no Host workspace")
		}
		snap := *job.Assignment.Workspace
		want := "original"
		if len(prior) > 0 {
			want = "first"
		}
		if string(snap.Files[0].Data) != want {
			t.Fatal("dependency files missing", snap)
		}
		var changes []WorkspaceChange
		if change {
			f := snap.Files[0]
			f.Data = []byte("first")
			changes = []WorkspaceChange{{File: f, Before: fileHash(snap.Files[0])}}
		}
		if _, e := broker.Handle(1, WorkerRequest{Action: "result", ID: inv.ID, JobID: job.JobID, Text: "done", Changes: changes}); e != nil {
			t.Fatal(e)
		}
		select {
		case e := <-errs:
			t.Fatal(e)
		case r := <-out:
			return r
		case <-ctx.Done():
			t.Fatal("stalled")
		}
		return Result{}
	}
	first := run(nil, true)
	second := run([]Result{first}, false)
	if first.WorkspacePath == second.WorkspacePath || first.WorkspacePath == root {
		t.Fatal("shared mutable workspace")
	}
	main, _ := os.ReadFile(filepath.Join(root, "code.txt"))
	a, _ := os.ReadFile(filepath.Join(first.WorkspacePath, "code.txt"))
	b, _ := os.ReadFile(filepath.Join(second.WorkspacePath, "code.txt"))
	if string(main) != "original" || string(a) != "first" || string(b) != "first" {
		t.Fatal("isolation failed")
	}
	writeFixture(t, root, "code.txt", "human change")
	_, e = broker.Run(context.Background(), member, Assignment{Prior: []Result{first}})
	if e == nil {
		t.Fatal("changed Host baseline silently overwritten")
	}
}
func TestRemoteWorkspacePermissionScope(t *testing.T) {
	b := NewRemoteBroker()
	if _, e := b.Handle(room.UID(1), WorkerRequest{Action: "invite", ID: strings.Repeat("a", 32), Provider: "cursor", Mode: "work", WorkspaceScope: "host"}); e == nil {
		t.Fatal("unsupported host silently accepted")
	}
}
