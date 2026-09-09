package resources

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var requestID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var accountID = regexp.MustCompile(`^ou_[A-Za-z0-9]{8,100}$`)

type FeishuIdentity struct {
	Name   string `json:"name"`
	OpenID string `json:"openId"`
}
type CreateReceipt struct {
	RequestID  string         `json:"requestId"`
	Title      string         `json:"title"`
	Account    FeishuIdentity `json:"account"`
	State      string         `json:"state"`
	URL        string         `json:"url,omitempty"`
	DocumentID string         `json:"documentId,omitempty"`
	CreatedAt  string         `json:"createdAt"`
}

func (e Executor) lark(ctx context.Context, args []string, input string) ([]byte, error) {
	if e.command != nil {
		return e.command(ctx, args, input)
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lark-cli", args...)
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(), "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1")
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	return out.b, err
}
func (e Executor) FeishuIdentity(ctx context.Context) (FeishuIdentity, error) {
	b, err := e.lark(ctx, []string{"whoami", "--as", "user"}, "")
	if err != nil {
		return FeishuIdentity{}, errors.New("无法读取本机飞书用户登录，请先在本机登录")
	}
	var v struct {
		Identity    string `json:"identity"`
		Available   bool   `json:"available"`
		TokenStatus string `json:"tokenStatus"`
		OnBehalfOf  struct {
			UserName string `json:"userName"`
			OpenID   string `json:"openId"`
		} `json:"onBehalfOf"`
	}
	if json.Unmarshal(b, &v) != nil || v.Identity != "user" || !v.Available || v.TokenStatus != "ready" || !accountID.MatchString(v.OnBehalfOf.OpenID) {
		return FeishuIdentity{}, errors.New("本机没有可用的飞书用户登录；不会使用 bot 或 Host 身份")
	}
	return FeishuIdentity{Name: v.OnBehalfOf.UserName, OpenID: v.OnBehalfOf.OpenID}, nil
}
func (e Executor) ledger() (*sql.DB, error) {
	if e.ReceiptDir == "" || !filepath.IsAbs(e.ReceiptDir) {
		return nil, errors.New("本机创建回执目录不可用")
	}
	if err := os.MkdirAll(e.ReceiptDir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(e.ReceiptDir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("回执目录必须由本机用户私有")
	}
	dbPath := filepath.Join(e.ReceiptDir, "creates.db")
	if st, err := os.Lstat(dbPath); err == nil && !st.Mode().IsRegular() {
		return nil, errors.New("回执数据库必须是本机普通文件")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS creates(id TEXT PRIMARY KEY,digest TEXT NOT NULL,receipt TEXT NOT NULL);`); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(filepath.Join(e.ReceiptDir, "creates.db"), 0600); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func (e Executor) Receipt(ctx context.Context, id string) (CreateReceipt, error) {
	if !requestID.MatchString(id) {
		return CreateReceipt{}, errors.New("invalid receipt ID")
	}
	db, err := e.ledger()
	if err != nil {
		return CreateReceipt{}, err
	}
	defer db.Close()
	var b string
	if err = db.QueryRowContext(ctx, "SELECT receipt FROM creates WHERE id=?", id).Scan(&b); err != nil {
		return CreateReceipt{}, err
	}
	var receipt CreateReceipt
	err = json.Unmarshal([]byte(b), &receipt)
	return receipt, err
}
func documentXML(title, content string) string {
	escape := func(s string) string { var b strings.Builder; xml.EscapeText(&b, []byte(s)); return b.String() }
	var b strings.Builder
	b.WriteString("<title>" + escape(title) + "</title>")
	// Treat all user/model content as text. Never expand XML, images, file imports or remote embeds.
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		b.WriteString("<p>" + escape(line) + "</p>")
	}
	return b.String()
}
func (e Executor) createDocument(ctx context.Context, r Request) (string, error) {
	db, err := e.ledger()
	if err != nil {
		return "", err
	}
	defer db.Close()
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	previous := func() (string, error) {
		var savedDigest, receipt string
		err := db.QueryRowContext(ctx, "SELECT digest,receipt FROM creates WHERE id=?", r.RequestID).Scan(&savedDigest, &receipt)
		if err == nil && savedDigest != digest {
			return "", errors.New("创建请求标识与原内容冲突，请查看原回执")
		}
		return receipt, err
	}
	if receipt, err := previous(); err == nil {
		return receipt, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	identity, err := e.FeishuIdentity(ctx)
	if err != nil {
		return "", err
	}
	if identity.OpenID != r.AccountID {
		return "", errors.New("本机飞书账号已变化，请重新核对；没有创建文档")
	}
	receipt := CreateReceipt{RequestID: r.RequestID, Title: r.Title, Account: identity, State: "unknown", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	initial, _ := json.Marshal(receipt)
	result, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO creates(id,digest,receipt)VALUES(?,?,?)", r.RequestID, digest, string(initial))
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return previous()
	}
	// Durable reservation precedes the external write. A crash, timeout or partial
	// creation stays unknown and is never automatically replayed.
	b, runErr := e.lark(ctx, []string{"docs", "+create", "--as", "user", "--doc-format", "xml", "--content", "-", "--json"}, documentXML(r.Title, r.Content))
	var v struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Data     struct {
			Document struct {
				ID  string `json:"document_id"`
				URL string `json:"url"`
			} `json:"document"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &v) == nil {
		if strings.HasPrefix(v.Data.Document.URL, "https://") && validDocument(v.Data.Document.URL) {
			receipt.URL = v.Data.Document.URL
		}
		if token.MatchString(v.Data.Document.ID) {
			receipt.DocumentID = v.Data.Document.ID
		}
		if runErr == nil && v.OK && v.Identity == "user" && receipt.URL != "" && receipt.DocumentID != "" {
			receipt.State = "completed"
		}
	}
	encoded, _ := json.Marshal(receipt)
	// Persist success even when the originating connection has just gone away.
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = db.ExecContext(saveCtx, "UPDATE creates SET receipt=? WHERE id=?", string(encoded), r.RequestID); err != nil {
		return string(initial), nil
	}
	return string(encoded), nil
}
