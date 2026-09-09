package personal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type GitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (i GitIdentity) Validate() error {
	if strings.TrimSpace(i.Name) != i.Name || i.Name == "" || len(i.Name) > 120 || len(i.Email) > 254 || !utf8.ValidString(i.Name+i.Email) || strings.ContainsAny(i.Name, "<>") {
		return errors.New("请填写有效的 Git 姓名和邮箱")
	}
	for _, r := range i.Name + i.Email {
		if unicode.IsControl(r) {
			return errors.New("Git 身份不能包含控制字符")
		}
	}
	a, e := mail.ParseAddress(i.Email)
	if e != nil || a.Address != i.Email || a.Name != "" {
		return errors.New("请填写有效的 Git 邮箱")
	}
	return nil
}

type CommitInput struct {
	Message      string   `json:"message"`
	Paths        []string `json:"paths"`
	ExpectedHead string   `json:"expectedHead"`
}
type CommitReceipt struct {
	ID     string      `json:"id"`
	State  string      `json:"state"`
	Commit string      `json:"commit,omitempty"`
	Branch string      `json:"branch"`
	Author GitIdentity `json:"author"`
	Error  string      `json:"error,omitempty"`
}

var gitOID = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func (in CommitInput) Validate() error {
	if strings.TrimSpace(in.Message) == "" || len(in.Message) > 2000 || !utf8.ValidString(in.Message) || strings.ContainsRune(in.Message, 0) || len(in.Paths) == 0 || len(in.Paths) > 100 || (in.ExpectedHead != "unborn" && !gitOID.MatchString(in.ExpectedHead)) {
		return errors.New("commit 需要提交说明、明确文件列表和当前 HEAD（空仓库使用 unborn）")
	}
	seen := map[string]bool{}
	for _, p := range in.Paths {
		if !filepath.IsLocal(p) || filepath.Clean(p) != p || len(p) > 512 || strings.ContainsAny(p, "\x00\r\n\\") || p == "." || !utf8.ValidString(p) || seen[p] {
			return errors.New("提交路径必须是明确的项目相对文件路径，不能使用目录或通配规则")
		}
		for _, part := range strings.Split(p, "/") {
			if strings.EqualFold(part, ".git") {
				return errors.New("不能提交 Git 内部路径")
			}
		}
		seen[p] = true
	}
	return nil
}

type commitPlan struct {
	root, temp, index, ref, base, tree, summary string
	input                                       CommitInput
	indexBytes                                  []byte
	indexExists                                 bool
}

func gitCommand(ctx context.Context, root, index, input string, identity *GitIdentity, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "core.fsmonitor=false", "--literal-pathspecs", "-C", root}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = time.Second
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	if identity != nil {
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME="+identity.Name, "GIT_AUTHOR_EMAIL="+identity.Email, "GIT_COMMITTER_NAME="+identity.Name, "GIT_COMMITTER_EMAIL="+identity.Email)
	}
	var out limitedGitOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", errors.New("Git 操作未完成：" + args[0] + "；请检查仓库状态，不会切换提交身份")
	}
	return out.String(), nil
}

type limitedGitOutput struct{ bytes.Buffer }

func (w *limitedGitOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 1<<20 {
		return 0, errors.New("Git 输出过大")
	}
	return w.Buffer.Write(p)
}
func gitPath(ctx context.Context, root, name string) (string, error) {
	v, e := gitCommand(ctx, root, "", "", nil, "rev-parse", "--git-path", name)
	p := strings.TrimSpace(v)
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return p, e
}
func readIndex(path string) ([]byte, bool, error) {
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil, false, nil
	}
	return b, e == nil, e
}
func prepareCommit(ctx context.Context, root string, in CommitInput) (*commitPlan, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	p := &commitPlan{root: root, input: in}
	fail := func(e error) (*commitPlan, error) {
		if p.temp != "" {
			os.RemoveAll(p.temp)
		}
		return nil, e
	}
	top, e := gitCommand(ctx, root, "", "", nil, "rev-parse", "--show-toplevel")
	if e != nil || filepath.Clean(strings.TrimSpace(top)) != filepath.Clean(root) {
		return nil, errors.New("Host 项目目录需要是 Git 仓库根目录")
	}
	ref, e := gitCommand(ctx, root, "", "", nil, "symbolic-ref", "--quiet", "HEAD")
	if e != nil || !strings.HasPrefix(strings.TrimSpace(ref), "refs/heads/") {
		return nil, errors.New("请先切换到本地分支，不能在 detached HEAD 上提交")
	}
	p.ref = strings.TrimSpace(ref)
	base, e := gitCommand(ctx, root, "", "", nil, "rev-parse", "--verify", "HEAD")
	p.base = strings.TrimSpace(base)
	if (e != nil && in.ExpectedHead != "unborn") || (e == nil && p.base != in.ExpectedHead) {
		return nil, errors.New("HEAD 已变化，请重新检查提交范围")
	}
	p.index, e = gitPath(ctx, root, "index")
	if e != nil {
		return nil, e
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		v, e := gitPath(ctx, root, name)
		if e != nil {
			return nil, e
		}
		if _, e = os.Stat(v); e == nil {
			return nil, errors.New("请先完成或退出当前 merge/rebase/cherry-pick 操作")
		}
	}
	p.indexBytes, p.indexExists, e = readIndex(p.index)
	if e != nil {
		return nil, e
	}
	unmerged, e := gitCommand(ctx, root, "", "", nil, "ls-files", "-u")
	if e != nil || unmerged != "" {
		return nil, errors.New("请先处理索引冲突")
	}
	for _, name := range in.Paths {
		// Reject directories and parent symlinks; leaf symlinks remain ordinary Git entries.
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			st, e := os.Lstat(filepath.Join(root, parent))
			if e == nil && st.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("提交路径父目录不能是符号链接")
			}
		}
		st, e := os.Lstat(filepath.Join(root, name))
		if e == nil && (st.IsDir() || (!st.Mode().IsRegular() && st.Mode()&os.ModeSymlink == 0)) {
			return nil, errors.New("请逐个指定普通文件或符号链接")
		}
		if e != nil && !os.IsNotExist(e) {
			return nil, e
		}
	}
	p.temp, e = os.MkdirTemp("", "agent-room-commit-")
	if e != nil {
		return nil, e
	}
	index := filepath.Join(p.temp, "snapshot-index")
	args := []string{"read-tree", "--empty"}
	if p.base != "" {
		args = []string{"read-tree", p.base}
	}
	if _, e = gitCommand(ctx, root, index, "", nil, args...); e != nil {
		return fail(e)
	}
	if _, e = gitCommand(ctx, root, index, "", nil, append([]string{"add", "-A", "--"}, in.Paths...)...); e != nil {
		return fail(e)
	}
	tree, e := gitCommand(ctx, root, index, "", nil, "write-tree")
	if e != nil {
		return fail(e)
	}
	p.tree = strings.TrimSpace(tree)
	diff, e := gitCommand(ctx, root, index, "", nil, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--stat")
	if e != nil {
		return fail(e)
	}
	if diff == "" {
		return fail(errors.New("指定文件没有可提交的修改"))
	}
	p.summary = "分支：" + p.ref + "\n基础 HEAD：" + in.ExpectedHead + "\n提交快照：" + p.tree + "\n文件范围：\n" + strings.Join(in.Paths, "\n") + "\n\n" + diff
	if len(p.summary) > 24000 {
		return fail(errors.New("提交范围过大，请分批提交"))
	}
	return p, nil
}
func (p *commitPlan) apply(ctx context.Context, identity GitIdentity) (hash string, err error) {
	if err = identity.Validate(); err != nil {
		return "", err
	}
	// Reserve the shared index against concurrent checkout/staging. update-ref
	// acquires its own ref locks and checks the expected old commit atomically.
	lock, e := os.OpenFile(p.index+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", errors.New("Git 索引正被其他操作使用")
	}
	defer lock.Close()
	defer os.Remove(p.index + ".lock")
	now, exists, e := readIndex(p.index)
	if e != nil || exists != p.indexExists || !bytes.Equal(now, p.indexBytes) {
		return "", errors.New("等待授权期间暂存区已变化，未提交")
	}
	ref, e := gitCommand(ctx, p.root, "", "", nil, "symbolic-ref", "--quiet", "HEAD")
	if e != nil || strings.TrimSpace(ref) != p.ref {
		return "", errors.New("当前分支已变化，未提交")
	}
	merged := filepath.Join(p.temp, "merged-index")
	if exists {
		if e = os.WriteFile(merged, now, 0600); e != nil {
			return "", e
		}
	} else {
		args := []string{"read-tree", "--empty"}
		if p.base != "" {
			args = []string{"read-tree", p.base}
		}
		if _, e = gitCommand(ctx, p.root, merged, "", nil, args...); e != nil {
			return "", e
		}
	}
	// Merge only the selected entries into the original index, preserving all others.
	var entries strings.Builder
	for _, path := range p.input.Paths {
		row, e := gitCommand(ctx, p.root, "", "", nil, "ls-tree", "-z", p.tree, "--", path)
		if e != nil {
			return "", e
		}
		if row == "" {
			entries.WriteString("0 " + strings.Repeat("0", len(p.tree)) + "\t" + path + "\x00")
			continue
		}
		tab := strings.IndexByte(row, '\t')
		if tab < 0 {
			return "", errors.New("invalid tree entry")
		}
		fields := strings.Fields(row[:tab])
		if len(fields) != 3 || fields[1] != "blob" {
			return "", errors.New("暂不支持提交子模块变更")
		}
		entries.WriteString(fields[0] + " " + fields[2] + row[tab:])
	}
	if _, e = gitCommand(ctx, p.root, merged, entries.String(), nil, "update-index", "-z", "--index-info"); e != nil {
		return "", e
	}
	data, e := os.ReadFile(merged)
	if e != nil {
		return "", e
	}
	if _, e = lock.Write(data); e != nil {
		return "", e
	}
	if e = lock.Sync(); e != nil {
		return "", e
	}
	args := []string{"commit-tree", p.tree, "--no-gpg-sign"}
	if p.base != "" {
		args = append(args, "-p", p.base)
	}
	raw, e := gitCommand(ctx, p.root, "", p.input.Message+"\n", &identity, args...)
	if e != nil {
		return "", e
	}
	hash = strings.TrimSpace(raw)
	if !gitOID.MatchString(hash) {
		return "", errors.New("invalid commit ID")
	}
	old := p.base
	if old == "" {
		old = strings.Repeat("0", len(hash))
	}
	if _, e = gitCommand(ctx, p.root, "", "", &identity, "update-ref", "-m", "agent_room personal commit", p.ref, hash, old); e != nil {
		return hash, e
	}
	if e = lock.Close(); e != nil {
		return hash, e
	}
	if e = os.Rename(p.index+".lock", p.index); e != nil {
		return hash, e
	}
	return hash, nil
}

func (b *Broker) Commit(ctx context.Context, capability, root, receiptDir string, in CommitInput) (CommitReceipt, error) {
	b.commitMu.Lock()
	defer b.commitMu.Unlock()
	if err := in.Validate(); err != nil {
		return CommitReceipt{}, err
	}
	b.mu.Lock()
	err := b.valid(ctx)
	if capability == "" || capability != b.capability {
		err = errors.New("提交凭证已失效")
	}
	message, uid := b.message, b.actor.UID
	b.mu.Unlock()
	if err != nil {
		return CommitReceipt{}, err
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("%s:%d:", message, uid)), raw...))
	id := fmt.Sprintf("%x", sum[:16])
	if !filepath.IsAbs(receiptDir) {
		return CommitReceipt{}, errors.New("回执目录不可用")
	}
	if err = os.MkdirAll(receiptDir, 0700); err != nil {
		return CommitReceipt{}, err
	}
	path := filepath.Join(receiptDir, id+".json")
	if data, e := os.ReadFile(path); e == nil {
		var r CommitReceipt
		if json.Unmarshal(data, &r) != nil {
			return r, errors.New("提交回执损坏，请手动核对")
		}
		return r, nil
	} else if !os.IsNotExist(e) {
		return CommitReceipt{}, e
	}
	p, err := prepareCommit(ctx, root, in)
	if err != nil {
		return CommitReceipt{}, err
	}
	defer os.RemoveAll(p.temp)
	result, err := b.call(ctx, capability, "git.commit", in.Message, p.summary)
	if err != nil {
		return CommitReceipt{}, err
	}
	if result.GitIdentity == nil {
		return CommitReceipt{}, errors.New("发送者未确认 Git 署名；没有提交")
	}
	author := *result.GitIdentity
	b.mu.Lock()
	if capability != b.capability {
		b.mu.Unlock()
		return CommitReceipt{}, errors.New("任务已变化，没有提交")
	}
	if err = b.valid(ctx); err != nil {
		b.mu.Unlock()
		return CommitReceipt{}, err
	}
	commitCtx, commitCancel := context.WithCancel(ctx)
	b.commitCancel = commitCancel
	b.mu.Unlock()
	defer func() { commitCancel(); b.mu.Lock(); b.commitCancel = nil; b.mu.Unlock() }()
	receipt := CommitReceipt{ID: id, State: "unknown", Author: author, Branch: p.ref, Error: "提交结果待核实，请检查分支和暂存区；不会自动重试"}
	reservation, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return receipt, err
	}
	data, _ := json.Marshal(receipt)
	_, err = reservation.Write(data)
	if err == nil {
		err = reservation.Sync()
	}
	closeErr := reservation.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return receipt, err
	}
	receipt.Commit, err = p.apply(commitCtx, author)
	if err != nil {
		receipt.Error = err.Error() + "；请核对分支及暂存区，不能据此自动重试"
	}
	if err == nil {
		receipt.State = "completed"
		receipt.Error = ""
	}
	data, _ = json.Marshal(receipt)
	f, e := os.CreateTemp(receiptDir, ".commit-")
	if e != nil {
		return receipt, nil
	}
	defer os.Remove(f.Name())
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	if e == nil {
		os.Rename(f.Name(), path)
	}
	return receipt, nil
}
