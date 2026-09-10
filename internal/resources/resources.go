// Package resources executes typed resource requests using local credentials.
// No command, environment, executable or credential is accepted from a remote peer.
package resources

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const MaxResult = 48000

type Request struct {
	Title     string `json:"title,omitempty"`
	Content   string `json:"content,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	AccountID string `json:"accountId,omitempty"`

	Action string `json:"action"`
	Target string `json:"target"`
	Query  string `json:"query,omitempty"`
}
type Execute func(context.Context, Request) (string, error)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var token = regexp.MustCompile(`^[A-Za-z0-9]{10,100}$`)
var issue = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[1-9][0-9]{0,9}$`)

func (r Request) Validate() error {
	if len(r.Target) > 1024 || len(r.Query) > 500 || !utf8.ValidString(r.Target+r.Query) || strings.ContainsAny(r.Target+r.Query, "\x00\r\n") {
		return errors.New("invalid resource request")
	}
	if r.IsWrite() {
		for _, c := range r.Title + r.Content {
			if (c < 32 && c != '\n' && c != '\r' && c != '\t') || c == 0xfffe || c == 0xffff {
				return errors.New("文档包含不支持的控制字符")
			}
		}
		if strings.TrimSpace(r.Title) == "" || len(r.Title) > 240 || len(r.Content) > 24000 || strings.TrimSpace(r.Content) == "" || !utf8.ValidString(r.Title+r.Content) || strings.ContainsAny(r.Title, "\x00\r\n") || strings.ContainsRune(r.Content, 0) || !requestID.MatchString(r.RequestID) || !accountID.MatchString(r.AccountID) || (r.Action == "feishu.create" && r.Target != "") || (r.Action == "feishu.append" && !ValidAppendTarget(r.Target)) || r.Query != "" {
			return errors.New("文档标题、正文、账号或请求标识无效")
		}
		return nil
	}
	if r.Title != "" || r.Content != "" || r.RequestID != "" || r.AccountID != "" {
		return errors.New("read operation contains write fields")
	}
	switch r.Action {
	case "github.repo", "github.issues", "github.search":
		if r.Action == "github.search" && (strings.TrimSpace(r.Query) == "" || strings.ContainsAny(r.Query, ":\"()") || strings.Contains(" "+strings.ToUpper(r.Query)+" ", " OR ")) {
			return errors.New("搜索词请使用普通关键词，不包含查询限定符")
		}
		if identifier.MatchString(r.Target) && !strings.Contains(r.Target, "..") {
			return nil
		}
	case "github.issue":
		if issue.MatchString(r.Target) && !strings.Contains(r.Target, "..") {
			return nil
		}
	case "feishu.document":
		if validDocument(r.Target) {
			return nil
		}
	case "feishu.search":
		if strings.TrimSpace(r.Query) != "" && r.Target == "" {
			return nil
		}
	case "project.file":
		if r.Target != "" && filepath.IsLocal(r.Target) && !strings.Contains(r.Target, "\\") {
			for _, part := range strings.Split(filepath.ToSlash(r.Target), "/") {
				if strings.HasPrefix(part, ".") || part == "node_modules" {
					return errors.New("hidden and dependency files are not allowed")
				}
			}
			return nil
		}
	}
	return errors.New("unsupported resource or invalid target")
}

type Executor struct {
	Root       string
	ReceiptDir string
	// Tests may substitute the local CLI transport, never supplied by network input.
	command func(context.Context, []string, string) ([]byte, error)
}

func (r Request) IsWrite() bool { return r.Action == "feishu.create" || r.Action == "feishu.append" }

// boundedOutput rejects oversized output instead of silently truncating evidence.
type boundedOutput struct{ b []byte }

func (w *boundedOutput) Write(p []byte) (int, error) {
	if len(w.b)+len(p) > MaxResult {
		return 0, errors.New("resource too large")
	}
	w.b = append(w.b, p...)
	return len(p), nil
}
func (e Executor) Run(ctx context.Context, r Request) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	if r.IsWrite() {
		return e.createDocument(ctx, r)
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if r.Action == "project.file" {
		if e.Root == "" || !filepath.IsAbs(e.Root) {
			return "", errors.New("请先在本机选择项目目录")
		}
		root, err := os.OpenRoot(e.Root)
		if err != nil {
			return "", errors.New("无法打开已授权项目目录")
		}
		defer root.Close()
		f, err := root.OpenFile(r.Target, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return "", errors.New("文件不存在或不在授权目录内")
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > MaxResult {
			return "", errors.New("仅支持小于 48 KB 的普通文本文件")
		}
		b, err := io.ReadAll(io.LimitReader(f, MaxResult+1))
		if err != nil || len(b) > MaxResult || !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
			return "", errors.New("文件不是可读取的文本")
		}
		return string(b), nil
	}
	name := "gh"
	var args []string
	switch r.Action {
	case "github.repo":
		args = []string{"api", "--hostname", "github.com", "--method", "GET", "repos/" + r.Target}
	case "github.issues":
		args = []string{"api", "--hostname", "github.com", "--method", "GET", "repos/" + r.Target + "/issues?state=open&per_page=20"}
	case "github.search":
		args = []string{"api", "--hostname", "github.com", "--method", "GET", "search/issues?per_page=20&q=" + url.QueryEscape("repo:"+r.Target+" "+r.Query)}
	case "github.issue":
		parts := strings.Split(r.Target, "#")
		args = []string{"api", "--hostname", "github.com", "--method", "GET", "repos/" + parts[0] + "/issues/" + parts[1]}
	case "feishu.document":
		name = "lark-cli"
		args = []string{"docs", "+fetch", "--as", "user", "--doc", r.Target, "--doc-format", "markdown", "--json"}
	case "feishu.search":
		name = "lark-cli"
		args = []string{"drive", "+search", "--as", "user", "--query", r.Query, "--page-size", "10", "--json"}
	}
	// The local operator may resolve a differently named GitHub CLI; never supplied by Host.
	if name == "gh" && os.Getenv("AGENT_ROOM_GITHUB_CLI") != "" {
		name = os.Getenv("AGENT_ROOM_GITHUB_CLI")
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = os.TempDir()
	env := []string{}
	for _, v := range os.Environ() {
		k := strings.SplitN(v, "=", 2)[0]
		if k != "GH_DEBUG" && k != "DEBUG" && k != "GH_REPO" {
			env = append(env, v)
		}
	}
	cmd.Env = append(env, "GH_PROMPT_DISABLED=1", "GH_PAGER=cat", "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1")
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return "", errors.New("本机工具调用失败：请检查 CLI 安装、登录、资源权限或结果大小；未改用其他身份")
	}
	if !utf8.Valid(out.b) {
		return "", errors.New("resource output is not UTF-8")
	}
	return string(out.b), nil
}

func validDocument(target string) bool {
	if token.MatchString(target) {
		return true
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, domain := range []string{"feishu.cn", "larkoffice.com", "larksuite.com", "doubao.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			allowed = true
		}
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return allowed && len(parts) == 2 && (parts[0] == "docx" || parts[0] == "wiki") && token.MatchString(parts[1])
}
