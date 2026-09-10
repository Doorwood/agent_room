package gitpush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), root, nil, []string{"GIT_AUTHOR_NAME=Sender", "GIT_AUTHOR_EMAIL=sender@example.test", "GIT_COMMITTER_NAME=Sender", "GIT_COMMITTER_EMAIL=sender@example.test"}, 48000, append([]string{"-c", "commit.gpgSign=false"}, args...)...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
func fixture(t *testing.T) (string, Input, Offer, []byte, string) {
	t.Helper()
	root := t.TempDir()
	testGit(t, root, "init", "--template=")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("first\n"), 0600)
	testGit(t, root, "add", "a.txt")
	testGit(t, root, "commit", "-m", "base")
	base := testGit(t, root, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("second\n"), 0600)
	testGit(t, root, "add", "a.txt")
	testGit(t, root, "commit", "-m", "next")
	in := Input{Repository: "sender/project", Branch: "feature/ui", Commit: testGit(t, root, "rev-parse", "HEAD")}
	o, p, e := Prepare(context.Background(), root, in)
	if e != nil {
		t.Fatal(e)
	}
	return root, in, o, p, base
}
func fakeExecutor(t *testing.T, in Input, remote *string, token *string, count *int) Executor {
	t.Helper()
	return Executor{ReceiptDir: filepath.Join(t.TempDir(), "private"), token: func(context.Context) (string, error) { return *token, nil }, api: func(_ context.Context, tok, path string, out any) error {
		if tok != "local-secret" {
			return errors.New("unauthenticated")
		}
		var v any
		switch {
		case path == "user":
			v = map[string]any{"id": 123, "login": "sender", "type": "User"}
		case path == "repos/"+in.Repository:
			v = map[string]any{"full_name": in.Repository, "permissions": map[string]bool{"push": true}}
		case strings.Contains(path, "/git/ref/heads/"):
			if *remote == "" {
				return errMissing
			}
			v = map[string]any{"object": map[string]string{"sha": *remote, "type": "commit"}}
		default:
			t.Fatal("unexpected API path", path)
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}, send: func(_ context.Context, _ string, got Input, old, tok string) error {
		*count++
		if got != in || tok != "local-secret" || old != *remote {
			t.Fatal("push request mutated")
		}
		*remote = in.Commit
		return nil
	}}
}
func TestPrepareImportsPinnedCommitWithoutWorkingTree(t *testing.T) {
	root, in, o, pack, _ := fixture(t)
	os.WriteFile(filepath.Join(root, "uncommitted.txt"), []byte("private draft"), 0600)
	dir, err := importPack(context.Background(), pack, o)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if got := testGit(t, dir, "show", in.Commit+":a.txt"); got != "second" {
		t.Fatal(got)
	}
	if _, err = git(context.Background(), dir, nil, nil, 256, "cat-file", "-e", in.Commit+":uncommitted.txt"); err == nil {
		t.Fatal("uncommitted file leaked")
	}
	if _, err = os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("checked out project code")
	}
	pack[10] ^= 1
	if _, err = importPack(context.Background(), pack, o); err == nil {
		t.Fatal("accepted tampered pack")
	}
	in.Commit = strings.Repeat("a", 40)
	if _, _, err = Prepare(context.Background(), root, in); err == nil {
		t.Fatal("accepted changed HEAD")
	}
}
func TestPushReceiptIdempotencyAndAccountBinding(t *testing.T) {
	_, in, o, pack, old := fixture(t)
	tok := "local-secret"
	count := 0
	e := fakeExecutor(t, in, &old, &tok, &count)
	id := strings.Repeat("a", 32)
	a, err := e.Check(context.Background(), id, o)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(a)
	if bytes.Contains(raw, []byte(tok)) {
		t.Fatal("token serialized")
	}
	tok = "other-account"
	if _, err = e.Execute(context.Background(), a, pack); err == nil || count != 0 {
		t.Fatal("account switch accepted")
	}
	tok = "local-secret"
	r, err := e.Execute(context.Background(), a, pack)
	if err != nil || r.State != "completed" || count != 1 {
		t.Fatal(r, err, count)
	}
	r, err = e.Execute(context.Background(), a, pack)
	if err != nil || r.State != "completed" || count != 1 {
		t.Fatal("repeated external write", r, err, count)
	}
	history, err := e.Recent(context.Background())
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
	data, err := os.ReadFile(filepath.Join(e.ReceiptDir, "pushes.db"))
	if err != nil || bytes.Contains(data, []byte(tok)) {
		t.Fatal("credential persisted", err)
	}
}
func TestPushRefChangeNonFastForwardCancellationAndUnknown(t *testing.T) {
	for _, scenario := range []string{"changed", "diverged", "cancelled", "unknown", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			_, in, o, pack, old := fixture(t)
			tok := "local-secret"
			count := 0
			e := fakeExecutor(t, in, &old, &tok, &count)
			if scenario == "diverged" {
				old = strings.Repeat("b", 40)
			}
			a, err := e.Check(context.Background(), strings.Repeat("b", 32), o)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "changed":
				old = strings.Repeat("c", 40)
			case "cancelled":
				cancel()
			case "unknown":
				e.send = func(context.Context, string, Input, string, string) error {
					count++
					return errors.New("network ended")
				}
			case "expired":
				a.checked = time.Now().Add(-6 * time.Minute)
			}
			r, err := e.Execute(ctx, a, pack)
			if scenario == "unknown" {
				if err != nil || r.State != "unknown" || count != 1 {
					t.Fatal(r, err, count)
				}
				e.Execute(context.Background(), a, pack)
				if count != 1 {
					t.Fatal("unknown push replayed")
				}
			} else if err == nil || count != 0 {
				t.Fatal("unsafe push", r, err, count)
			}
		})
	}
}
func TestPushLeaseAgainstRealTemporaryRemote(t *testing.T) {
	root, in, o, pack, base := fixture(t)
	imported, err := importPack(context.Background(), pack, o)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(imported)
	remote := t.TempDir()
	testGit(t, remote, "init", "--bare", "--template=")
	execute := func(old string) error {
		args := pushArguments("/unused-helper", in, old)
		args[len(args)-2] = remote
		_, e := git(context.Background(), imported, nil, nil, 48000, append([]string{"-c", "protocol.file.allow=always"}, args...)...)
		return e
	}
	if err = execute(""); err != nil {
		t.Fatal("new branch", err)
	}
	if got := testGit(t, remote, "rev-parse", in.Ref()); got != in.Commit {
		t.Fatal(got)
	}
	testGit(t, remote, "update-ref", in.Ref(), base)
	if err = execute(base); err != nil {
		t.Fatal("fast forward", err)
	}
	// Another writer advanced the remote after this request captured base.
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("third\n"), 0600)
	testGit(t, root, "add", "a.txt")
	testGit(t, root, "commit", "-m", "other writer")
	third := testGit(t, root, "rev-parse", "HEAD")
	testGit(t, root, "-c", "protocol.file.allow=always", "push", remote, third+":"+in.Ref())
	if err = execute(base); err == nil {
		t.Fatal("lease allowed overwriting changed branch")
	}
	if got := testGit(t, remote, "rev-parse", in.Ref()); got != third {
		t.Fatal("remote overwritten")
	}
}
func TestTargetsAndCredentialHelperCannotLeakToOtherHosts(t *testing.T) {
	good := Input{Repository: "owner/repo", Branch: "main", Commit: strings.Repeat("a", 40)}
	for _, v := range []string{"https://evil.test/x/y", "owner/../repo", "owner/repo.git", "owner/.", "-evil/repo", "owner/repo\n"} {
		in := good
		in.Repository = v
		if in.Validate() == nil {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"--force", "../main", "main.lock", "a//b", "refs/heads/../main", "main:other", "main\n", "a/.b"} {
		in := good
		in.Branch = v
		if in.Validate() == nil {
			t.Fatal(v)
		}
	}
	for _, input := range []string{"protocol=https\nhost=evil.test\npath=owner/repo.git\n\n", "protocol=http\nhost=github.com\npath=owner/repo.git\n\n", "protocol=https\nhost=github.com\npath=other/repo.git\n\n"} {
		var out bytes.Buffer
		if Credential(strings.NewReader(input), &out, "get", "secret", "owner/repo") == nil || out.Len() != 0 {
			t.Fatal("credential leaked")
		}
	}
	var out bytes.Buffer
	if err := Credential(strings.NewReader("protocol=https\nhost=github.com\npath=owner/repo.git\n\n"), &out, "get", "secret", "owner/repo"); err != nil || !strings.Contains(out.String(), "password=secret") {
		t.Fatal(err)
	}
	args := strings.Join(pushArguments("/local/bin/agent room", good, ""), " ")
	if strings.Contains(args, "secret") || !strings.Contains(args, "--force-with-lease=refs/heads/main:") || !strings.Contains(args, good.URL()) {
		t.Fatal(args)
	}
}

func TestPushCheckRejectsWrongIdentityAndRepositoryPermissions(t *testing.T) {
	_, in, o, _, old := fixture(t)
	for _, scenario := range []string{"bot", "readonly", "archived", "different-repository"} {
		t.Run(scenario, func(t *testing.T) {
			tok := "local-secret"
			count := 0
			e := fakeExecutor(t, in, &old, &tok, &count)
			base := e.api
			e.api = func(ctx context.Context, token, path string, out any) error {
				if path == "user" && scenario == "bot" {
					return json.Unmarshal([]byte(`{"id":1,"login":"bot-user","type":"Bot"}`), out)
				}
				if path == "repos/"+in.Repository {
					name := in.Repository
					if scenario == "different-repository" {
						name = "other/project"
					}
					raw, _ := json.Marshal(map[string]any{"full_name": name, "archived": scenario == "archived", "permissions": map[string]bool{"push": scenario != "readonly"}})
					return json.Unmarshal(raw, out)
				}
				return base(ctx, token, path, out)
			}
			if _, err := e.Check(context.Background(), strings.Repeat("c", 32), o); err == nil {
				t.Fatal("accepted", scenario)
			}
			if count != 0 {
				t.Fatal("check performed write")
			}
		})
	}
}
