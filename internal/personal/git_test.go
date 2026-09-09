package personal

import (
	"agent_romm/internal/room"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", root, "-c", "commit.gpgSign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Host", "GIT_AUTHOR_EMAIL=host@example.test", "GIT_COMMITTER_NAME=Host", "GIT_COMMITTER_EMAIL=host@example.test")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s: %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}
func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testGit(t, root, "init")
	for _, name := range []string{"one.go", "other.go"} {
		os.WriteFile(filepath.Join(root, name), []byte("initial\n"), 0600)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	return root
}
func TestCommitUsesSenderWithoutTouchingOtherStagedChanges(t *testing.T) {
	root := gitFixture(t)
	ctx := context.Background()
	head := testGit(t, root, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(root, "one.go"), []byte("Alice change\n"), 0600)
	os.WriteFile(filepath.Join(root, "other.go"), []byte("Bob staged\n"), 0600)
	testGit(t, root, "add", "other.go")
	testGit(t, root, "config", "user.name", "Host Config")
	testGit(t, root, "config", "user.email", "host-config@example.test")
	p, e := prepareCommit(ctx, root, CommitInput{Message: "Alice commit", Paths: []string{"one.go"}, ExpectedHead: head})
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(p.temp)
	// Later work stays uncommitted; only the prepared snapshot is committed.
	os.WriteFile(filepath.Join(root, "one.go"), []byte("later edit\n"), 0600)
	hash, e := p.apply(ctx, GitIdentity{Name: "Alice", Email: "alice@example.test"})
	if e != nil {
		t.Fatal(e)
	}
	if hash != testGit(t, root, "rev-parse", "HEAD") {
		t.Fatal("branch not advanced")
	}
	if got := testGit(t, root, "show", "-s", "--format=%an <%ae>|%cn <%ce>"); got != "Alice <alice@example.test>|Alice <alice@example.test>" {
		t.Fatal(got)
	}
	if got := testGit(t, root, "show", "HEAD:one.go"); got != "Alice change" {
		t.Fatal(got)
	}
	if got := testGit(t, root, "show", "HEAD:other.go"); got != "initial" {
		t.Fatal("committed Bob changes", got)
	}
	if got := testGit(t, root, "diff", "--cached", "--name-only"); got != "other.go" {
		t.Fatal("changed other staging", got)
	}
	if got := testGit(t, root, "diff", "--name-only"); got != "one.go" {
		t.Fatal("later work lost", got)
	}
	if got := testGit(t, root, "config", "user.name"); got != "Host Config" {
		t.Fatal("mutated shared identity")
	}
}
func TestCommitRejectsChangedIndexAndHead(t *testing.T) {
	for _, variant := range []string{"index", "head"} {
		t.Run(variant, func(t *testing.T) {
			root := gitFixture(t)
			base := testGit(t, root, "rev-parse", "HEAD")
			os.WriteFile(filepath.Join(root, "one.go"), []byte("change\n"), 0600)
			p, e := prepareCommit(context.Background(), root, CommitInput{Message: "change", Paths: []string{"one.go"}, ExpectedHead: base})
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(p.temp)
			if variant == "index" {
				testGit(t, root, "add", "one.go")
			} else {
				testGit(t, root, "commit", "--allow-empty", "-m", "other commit")
			}
			expected := testGit(t, root, "rev-parse", "HEAD")
			if _, e = p.apply(context.Background(), GitIdentity{Name: "Alice", Email: "alice@example.test"}); e == nil {
				t.Fatal("stale snapshot committed")
			}
			if got := testGit(t, root, "rev-parse", "HEAD"); got != expected {
				t.Fatal("changed HEAD")
			}
		})
	}
}
func TestCommitAddsDeletesAndInitialCommit(t *testing.T) {
	root := t.TempDir()
	testGit(t, root, "init")
	os.WriteFile(filepath.Join(root, "new.go"), []byte("new\n"), 0600)
	p, e := prepareCommit(context.Background(), root, CommitInput{Message: "first", Paths: []string{"new.go"}, ExpectedHead: "unborn"})
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(p.temp)
	if _, e = p.apply(context.Background(), GitIdentity{Name: "First", Email: "first@example.test"}); e != nil {
		t.Fatal(e)
	}
	base := testGit(t, root, "rev-parse", "HEAD")
	os.Remove(filepath.Join(root, "new.go"))
	os.WriteFile(filepath.Join(root, "added.go"), []byte("added\n"), 0600)
	p, e = prepareCommit(context.Background(), root, CommitInput{Message: "replace", Paths: []string{"new.go", "added.go"}, ExpectedHead: base})
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(p.temp)
	if _, e = p.apply(context.Background(), GitIdentity{Name: "Second", Email: "second@example.test"}); e != nil {
		t.Fatal(e)
	}
	if got := testGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatal(got)
	}
}
func TestCommitBrokerIdentityAndDurableReplay(t *testing.T) {
	root := gitFixture(t)
	dir := t.TempDir()
	head := testGit(t, root, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(root, "one.go"), []byte("change\n"), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := New(nil)
	b.SetMode("personal")
	cap, _ := b.Bind(room.ClientMessageID(strings.Repeat("a", 32)), room.Actor{UID: 1, Name: "alice"})
	in := CommitInput{Message: "delegated commit", Paths: []string{"one.go"}, ExpectedHead: head}
	out := make(chan CommitReceipt, 1)
	go func() {
		r, e := b.Commit(ctx, cap, root, dir, in)
		if e != nil {
			t.Error(e)
		}
		out <- r
	}()
	p := pendingFor(t, b, 1, strings.Repeat("c", 32))
	if p.Action != "git.commit" {
		t.Fatal(p)
	}
	if err := b.Resolve(ctx, 1, strings.Repeat("c", 32), Result{ID: p.ID, GitIdentity: &GitIdentity{Name: "Alice", Email: "alice@example.test"}}); err != nil {
		t.Fatal(err)
	}
	r := <-out
	if r.State != "completed" {
		t.Fatal(r)
	}
	again, e := b.Commit(ctx, cap, root, dir, in)
	if e != nil || again.Commit != r.Commit {
		t.Fatal("duplicate failed", again, e)
	}
	if got := testGit(t, root, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatal("duplicate commit", got)
	}
	if _, e = b.Commit(ctx, strings.Repeat("b", 64), root, dir, in); e == nil {
		t.Fatal("foreign capability read receipt")
	}
	bobCap, _ := b.Bind(room.ClientMessageID(strings.Repeat("b", 32)), room.Actor{UID: 2, Name: "bob"})
	os.WriteFile(filepath.Join(root, "one.go"), []byte("Bob change\n"), 0600)
	bobIn := CommitInput{Message: "Bob commit", Paths: []string{"one.go"}, ExpectedHead: r.Commit}
	go func() {
		r, e := b.Commit(ctx, bobCap, root, dir, bobIn)
		if e != nil {
			t.Error(e)
		}
		out <- r
	}()
	bp := pendingFor(t, b, 2, strings.Repeat("d", 32))
	if b.Resolve(ctx, 1, strings.Repeat("c", 32), Result{ID: bp.ID, GitIdentity: &GitIdentity{Name: "Alice", Email: "alice@example.test"}}) == nil {
		t.Fatal("Alice supplied Bob identity")
	}
	if err := b.Resolve(ctx, 2, strings.Repeat("d", 32), Result{ID: bp.ID, GitIdentity: &GitIdentity{Name: "Bob", Email: "bob@example.test"}}); err != nil {
		t.Fatal(err)
	}
	br := <-out
	if br.State != "completed" || br.Author.Name != "Bob" {
		t.Fatal(br)
	}
	if got := testGit(t, root, "log", "-2", "--format=%an <%ae>"); got != "Bob <bob@example.test>\nAlice <alice@example.test>" {
		t.Fatal("sender identities mixed", got)
	}

}
func TestGitIdentityAndCommitInputRejectInjection(t *testing.T) {
	for _, id := range []GitIdentity{{Name: "Alice\nBob", Email: "a@example.test"}, {Name: "A", Email: "A <a@example.test>"}, {Name: "A", Email: "a@example.test\nGIT_DIR=/tmp"}, {Name: "<Host>", Email: "a@example.test"}} {
		if id.Validate() == nil {
			t.Fatal(id)
		}
	}
	for _, path := range []string{".", "../file", ".git/config", "a/../b", "/etc/passwd", "a\nb"} {
		if (CommitInput{Message: "test", Paths: []string{path}, ExpectedHead: "unborn"}).Validate() == nil {
			t.Fatal(path)
		}
	}
}
