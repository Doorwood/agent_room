package gitpush

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Approval struct {
	ID             string  `json:"id"`
	Offer          Offer   `json:"offer"`
	Account        Account `json:"account"`
	ExpectedRemote string  `json:"expectedRemote"`
	token          string
	checked        time.Time
}
type Executor struct {
	ReceiptDir string
	// Substitutions are only provided by local Go tests, never remote input.
	token func(context.Context) (string, error)
	api   func(context.Context, string, string, any) error
	send  func(context.Context, string, Input, string, string) error
}

func (e Executor) localToken(ctx context.Context) (string, error) {
	if e.token != nil {
		return e.token(ctx)
	}
	name := os.Getenv("AGENT_ROOM_GITHUB_CLI")
	if name == "" {
		name = "gh"
	}
	env := []string{"GH_PROMPT_DISABLED=1", "GH_PAGER=cat"}
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_CONFIG_DIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	b, err := run(ctx, name, []string{"auth", "token", "--hostname", "github.com"}, os.TempDir(), nil, env, 8192)
	token := strings.TrimSpace(string(b))
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("请在自己的终端安装 GitHub CLI 并执行 gh auth login --hostname github.com --web")
	}
	return token, nil
}

var errMissing = errors.New("GitHub ref not found")

func (e Executor) get(ctx context.Context, token, path string, out any) error {
	if e.api != nil {
		return e.api(ctx, token, path, out)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	c := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := c.Do(req)
	if err != nil {
		return errors.New("GitHub 查询未完成，请检查本机网络")
	}
	defer r.Body.Close()
	if r.StatusCode == 404 {
		return errMissing
	}
	if r.StatusCode != 200 {
		return errors.New("GitHub 拒绝访问，请检查本机登录、仓库权限或组织授权")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 48001))
	if err != nil || len(b) > 48000 {
		return errors.New("GitHub 响应无效")
	}
	return json.Unmarshal(b, out)
}
func (e Executor) account(ctx context.Context, token string) (Account, error) {
	var a struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	err := e.get(ctx, token, "user", &a)
	identity := Account{ID: a.ID, Login: a.Login}
	if err != nil || a.Type != "User" || !identity.Valid() {
		return Account{}, errors.New("没有可验证的本机 GitHub 用户身份，请本人重新登录")
	}
	return identity, nil
}
func (e Executor) remote(ctx context.Context, token string, in Input) (string, error) {
	var v struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	err := e.get(ctx, token, "repos/"+in.Repository+"/git/ref/heads/"+url.PathEscape(in.Branch), &v)
	if errors.Is(err, errMissing) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if v.Object.Type != "commit" || !oid.MatchString(v.Object.SHA) {
		return "", errors.New("远端分支响应无效")
	}
	return v.Object.SHA, nil
}
func (e Executor) Check(ctx context.Context, id string, o Offer) (*Approval, error) {
	if !requestID.MatchString(id) {
		return nil, errors.New("invalid request ID")
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if e.send == nil {
		if err := isolatedGit(ctx); err != nil {
			return nil, err
		}
	}
	token, err := e.localToken(ctx)
	if err != nil {
		return nil, err
	}
	a, err := e.account(ctx, token)
	if err != nil {
		return nil, err
	}
	var repo struct {
		FullName    string `json:"full_name"`
		Archived    bool   `json:"archived"`
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err = e.get(ctx, token, "repos/"+o.Input.Repository, &repo); err != nil {
		return nil, err
	}
	if !strings.EqualFold(repo.FullName, o.Input.Repository) || repo.Archived || !repo.Permissions.Push {
		return nil, errors.New("该 GitHub 用户没有目标仓库写权限，或仓库已经归档")
	}
	old, err := e.remote(ctx, token, o.Input)
	if err != nil {
		return nil, err
	}
	return &Approval{ID: id, Offer: o, Account: a, ExpectedRemote: old, token: token, checked: time.Now()}, nil
}
func (e Executor) ledger() (*sql.DB, error) {
	if !filepath.IsAbs(e.ReceiptDir) {
		return nil, errors.New("推送回执目录不可用")
	}
	if err := os.MkdirAll(e.ReceiptDir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(e.ReceiptDir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("推送回执目录必须为本机私有目录")
	}
	path := filepath.Join(e.ReceiptDir, "pushes.db")
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		return nil, errors.New("推送回执文件不安全")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS pushes(id TEXT PRIMARY KEY,digest TEXT NOT NULL,receipt TEXT NOT NULL);`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func offerDigest(o Offer) string {
	raw, _ := json.Marshal(o)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (e Executor) Receipt(ctx context.Context, id string, o Offer) (*Receipt, error) {
	db, err := e.ledger()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var digest, raw string
	err = db.QueryRowContext(ctx, "SELECT digest,receipt FROM pushes WHERE id=?", id).Scan(&digest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Receipt
	if digest != offerDigest(o) || json.Unmarshal([]byte(raw), &r) != nil || !r.Valid(id, o.Input) {
		return nil, errors.New("推送回执与当前请求不一致，不能重试")
	}
	return &r, nil
}
func (e Executor) Execute(ctx context.Context, a *Approval, pack []byte) (Receipt, error) {
	if a == nil || !requestID.MatchString(a.ID) || !a.Account.Valid() || a.token == "" || time.Since(a.checked) > 5*time.Minute {
		return Receipt{}, errors.New("请重新核对并确认 GitHub 账号")
	}
	if r, err := e.Receipt(ctx, a.ID, a.Offer); err != nil {
		return Receipt{}, err
	} else if r != nil {
		return *r, nil
	}
	if err := a.Offer.CheckPack(pack); err != nil {
		return Receipt{}, err
	}
	token, err := e.localToken(ctx)
	if err != nil || token != a.token {
		return Receipt{}, errors.New("本机 GitHub 登录已变化，请重新核对账号")
	}
	account, err := e.account(ctx, a.token)
	if err != nil || account != a.Account {
		return Receipt{}, errors.New("GitHub 身份已变化，未推送")
	}
	root, err := importPack(ctx, pack, a.Offer)
	if err != nil {
		return Receipt{}, err
	}
	defer os.RemoveAll(root)
	// Check fast-forward locally before reserving an external write.
	if a.ExpectedRemote != "" && a.ExpectedRemote != a.Offer.Input.Commit {
		if _, err = git(ctx, root, nil, nil, 256, "merge-base", "--is-ancestor", a.ExpectedRemote, a.Offer.Input.Commit); err != nil {
			return Receipt{}, errors.New("远端包含本次提交未合并的历史，拒绝覆盖")
		}
	}
	old, err := e.remote(ctx, a.token, a.Offer.Input)
	if err != nil {
		return Receipt{}, err
	}
	if old != a.ExpectedRemote {
		return Receipt{}, errors.New("确认后远端分支已变化，请重新核对")
	}
	if err = ctx.Err(); err != nil {
		return Receipt{}, err
	}
	db, err := e.ledger()
	if err != nil {
		return Receipt{}, err
	}
	defer db.Close()
	r := Receipt{ID: a.ID, Input: a.Offer.Input, Account: a.Account, ExpectedRemote: old, State: "unknown", Error: "推送结果待核实；请检查远端分支，不会自动重试"}
	initial, _ := json.Marshal(r)
	res, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO pushes(id,digest,receipt)VALUES(?,?,?)", a.ID, offerDigest(a.Offer), string(initial))
	if err != nil {
		return Receipt{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Receipt{}, err
	}
	if n == 0 {
		saved, e := e.Receipt(ctx, a.ID, a.Offer)
		if e != nil {
			return Receipt{}, e
		}
		if saved == nil {
			return Receipt{}, errors.New("回执不可用")
		}
		return *saved, nil
	}
	send := e.send
	if send == nil {
		send = push
	}
	err = send(ctx, root, a.Offer.Input, old, a.token)
	if err == nil {
		current, e := e.remote(ctx, a.token, a.Offer.Input)
		if e == nil && current == a.Offer.Input.Commit {
			r.State = "completed"
			r.Error = ""
			r.URL = "https://github.com/" + r.Input.Repository + "/commit/" + r.Input.Commit
		}
	}
	// Persist even if the requesting connection has already ended; no credentials are recorded.
	encoded, _ := json.Marshal(r)
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = db.ExecContext(saveCtx, "UPDATE pushes SET receipt=? WHERE id=?", string(encoded), a.ID); err != nil {
		return Receipt{ID: r.ID, Input: r.Input, Account: r.Account, ExpectedRemote: r.ExpectedRemote, State: "unknown", Error: "回执保存失败，请检查远端分支"}, nil
	}
	return r, nil
}

// SameOffer compares exact approved metadata, excluding no fields.
func SameOffer(a, b Offer) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (e Executor) Recent(ctx context.Context) ([]Receipt, error) {
	db, err := e.ledger()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT receipt FROM pushes ORDER BY rowid DESC LIMIT 10")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Receipt{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r Receipt
		if json.Unmarshal([]byte(raw), &r) != nil || !r.Valid(r.ID, r.Input) {
			return nil, errors.New("推送回执损坏")
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
