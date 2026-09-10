package gitpush

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type limited struct {
	bytes.Buffer
	max int
}

func (b *limited) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("Git output too large")
	}
	return b.Buffer.Write(p)
}
func environment(extra []string) []string {
	var env []string
	for _, v := range os.Environ() {
		k := strings.SplitN(v, "=", 2)[0]
		if strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "GH_") || strings.HasPrefix(k, "GITHUB_") || strings.HasPrefix(k, "AGENT_ROOM_PUSH_") || k == "DEBUG" {
			continue
		}
		env = append(env, v)
	}
	return append(append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0"), extra...)
}
func run(ctx context.Context, name string, args []string, dir string, input []byte, env []string, max int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = environment(env)
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out := &limited{max: max}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, errors.New("本机 Git/GitHub 操作未完成，请检查登录、分支规则或仓库权限")
	}
	return out.Bytes(), nil
}
func git(ctx context.Context, root string, input []byte, env []string, max int, args ...string) ([]byte, error) {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "credential.useHttpPath=true", "-c", "http.followRedirects=false", "-c", "http.sslVerify=true", "-c", "push.followTags=false", "-c", "push.recurseSubmodules=no", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "gc.auto=0", "-C", root}
	return run(ctx, "git", append(prefix, args...), root, input, env, max)
}

// Prepare performs no network requests and includes only objects reachable from the pinned HEAD.
func Prepare(ctx context.Context, root string, in Input) (Offer, []byte, error) {
	if err := in.Validate(); err != nil {
		return Offer{}, nil, err
	}
	head, err := git(ctx, root, nil, nil, 256, "-c", "protocol.https.allow=never", "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != in.Commit {
		return Offer{}, nil, errors.New("HEAD 已变化，请核对要推送的 commit")
	}
	summary, err := git(ctx, root, nil, nil, 12000, "-c", "protocol.https.allow=never", "show", "--no-ext-diff", "--no-textconv", "--stat", "--format=fuller", in.Commit, "--")
	if err != nil {
		return Offer{}, nil, err
	}
	pack, err := git(ctx, root, []byte(in.Commit+"\n"), nil, MaxPack, "-c", "protocol.https.allow=never", "pack-objects", "--stdout", "--revs")
	if err != nil {
		return Offer{}, nil, errors.New("提交包生成失败或超过 32 MiB；请缩小仓库历史后在本机推送")
	}
	sum := sha256.Sum256(pack)
	return Offer{Input: in, PackSize: len(pack), PackSHA256: hex.EncodeToString(sum[:]), Summary: string(summary)}, pack, nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Credential only runs as a Git credential helper inside the local push subprocess.
// Tokens are passed through that process's environment, never saved or returned to Host.
func Credential(in io.Reader, out io.Writer, op, token, repository string) error {
	if op != "get" {
		return nil
	}
	if token == "" || strings.ContainsAny(token, "\r\n\x00") || !repo.MatchString(repository) {
		return errors.New("credential unavailable")
	}
	b, err := io.ReadAll(io.LimitReader(in, 8193))
	if err != nil || len(b) > 8192 {
		return errors.New("invalid credential request")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || values[k] != "" {
			return errors.New("invalid credential request")
		}
		values[k] = v
	}
	if values["protocol"] != "https" || values["host"] != "github.com" || values["path"] != repository+".git" {
		return errors.New("credential target refused")
	}
	_, err = io.WriteString(out, "username=x-access-token\npassword="+token+"\n\n")
	return err
}
func importPack(ctx context.Context, pack []byte, offer Offer) (string, error) {
	if err := offer.CheckPack(pack); err != nil {
		return "", err
	}
	root, err := os.MkdirTemp("", "agent-room-push-")
	if err != nil {
		return "", err
	}
	fail := func(e error) (string, error) { os.RemoveAll(root); return "", e }
	if _, err = git(ctx, root, nil, nil, 4096, "init", "--bare", "--template=", root); err != nil {
		return fail(err)
	}
	if _, err = git(ctx, root, pack, nil, 4096, "index-pack", "--stdin", "--strict"); err != nil {
		return fail(err)
	}
	typ, err := git(ctx, root, nil, nil, 256, "cat-file", "-t", offer.Input.Commit)
	if err != nil || strings.TrimSpace(string(typ)) != "commit" {
		return fail(errors.New("提交包不包含所确认的 commit"))
	}
	if _, err = git(ctx, root, nil, nil, 4096, "rev-list", "--objects", "--quiet", offer.Input.Commit); err != nil {
		return fail(err)
	}
	return root, nil
}
func push(ctx context.Context, root string, in Input, old, token string) error {
	if err := isolatedGit(ctx); err != nil {
		return err
	}
	if old != "" && old != in.Commit {
		if _, err := git(ctx, root, nil, nil, 256, "merge-base", "--is-ancestor", old, in.Commit); err != nil {
			return errors.New("远端历史不是本次 commit 的祖先，拒绝覆盖；请先合并远端变更")
		}
	}
	if old == in.Commit {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	// Reject configured URL rewriting before handing Git a credential helper.
	actual, err := git(ctx, root, nil, nil, 4096, "ls-remote", "--get-url", in.URL())
	if err != nil || strings.TrimSpace(string(actual)) != in.URL() {
		return errors.New("Git URL 重写与确认的 GitHub 仓库不一致")
	}
	_, err = git(ctx, root, nil, []string{"AGENT_ROOM_PUSH_TOKEN=" + token, "AGENT_ROOM_PUSH_REPOSITORY=" + in.Repository}, 48000, pushArguments(executable, in, old)...)

	return err
}

func pushArguments(executable string, in Input, old string) []string {
	helper := "!" + shellQuote(executable) + " personal-git-credential"
	return []string{"-c", "credential.helper=" + helper, "push", "--porcelain", "--no-verify", "--no-signed", "--force-with-lease=" + in.Ref() + ":" + old, in.URL(), in.Commit + ":" + in.Ref()}
}

// Git before 2.32 ignores GIT_CONFIG_GLOBAL. Refuse it rather than inheriting
// unrelated credential helpers, HTTP headers, URL rewrites or signing settings.
func isolatedGit(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "agent-room-git-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config")
	if err = os.WriteFile(path, []byte("[agentroomprobe]\n value = isolated\n"), 0600); err != nil {
		return err
	}
	b, err := git(ctx, dir, nil, []string{"GIT_CONFIG_GLOBAL=" + path}, 256, "config", "--get", "agentroomprobe.value")
	if err != nil || strings.TrimSpace(string(b)) != "isolated" {
		return errors.New("个人推送需要本机 Git 2.32 或更新版本，以隔离已有的凭据和网络配置；请升级 Git 后重试")
	}
	return nil
}
