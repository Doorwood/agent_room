// Package gitpush transfers immutable commits and executes pushes on the sender's machine.
package gitpush

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

const MaxPack = 32 << 20
const ChunkSize = 128 << 10

var oid = regexp.MustCompile(`^[a-f0-9]{40}$`)
var requestID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var repo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)
var branch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,199}$`)
var login = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

type Input struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
}

func (in Input) Validate() error {
	if !repo.MatchString(in.Repository) || strings.HasSuffix(in.Repository, "/.") || strings.Contains(in.Repository, "..") || strings.HasSuffix(strings.ToLower(in.Repository), ".git") || !branch.MatchString(in.Branch) || strings.Contains(in.Branch, "..") || strings.Contains(in.Branch, "//") || !oid.MatchString(in.Commit) {
		return errors.New("推送需要 github.com 的 owner/repo、明确分支和完整 SHA-1 commit")
	}
	for _, p := range strings.Split(in.Branch, "/") {
		if p == "" || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") || strings.HasSuffix(p, ".lock") {
			return errors.New("无效的分支名称")
		}
	}
	return nil
}
func (in Input) Ref() string { return "refs/heads/" + in.Branch }
func (in Input) URL() string { return "https://github.com/" + in.Repository + ".git" }

type Offer struct {
	Input      Input  `json:"input"`
	PackSize   int    `json:"packSize"`
	PackSHA256 string `json:"packSha256"`
	Summary    string `json:"summary"`
}

func (o Offer) Validate() error {
	if err := o.Input.Validate(); err != nil {
		return err
	}
	b, e := hex.DecodeString(o.PackSHA256)
	if e != nil || len(b) != 32 || len(o.PackSHA256) != 64 || o.PackSize < 32 || o.PackSize > MaxPack || len(o.Summary) > 12000 {
		return errors.New("无效或过大的提交包")
	}
	return nil
}
func (o Offer) CheckPack(b []byte) error {
	if err := o.Validate(); err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if len(b) != o.PackSize || hex.EncodeToString(sum[:]) != o.PackSHA256 {
		return errors.New("提交包长度或校验值不匹配，未推送")
	}
	return nil
}

type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

func (a Account) Valid() bool { return a.ID > 0 && login.MatchString(a.Login) }

type Receipt struct {
	ID             string  `json:"id"`
	Input          Input   `json:"input"`
	Account        Account `json:"account"`
	ExpectedRemote string  `json:"expectedRemote"`
	State          string  `json:"state"`
	URL            string  `json:"url,omitempty"`
	Error          string  `json:"error,omitempty"`
}

func (r Receipt) Valid(id string, in Input) bool {
	return in.Validate() == nil && requestID.MatchString(id) && r.ID == id && r.Input == in && r.Account.Valid() && (r.ExpectedRemote == "" || oid.MatchString(r.ExpectedRemote)) && (r.State == "completed" || r.State == "unknown" || r.State == "rejected") && (r.State != "completed" || r.URL == "https://github.com/"+in.Repository+"/commit/"+in.Commit) && (r.URL == "" || r.URL == "https://github.com/"+in.Repository+"/commit/"+in.Commit) && len(r.Error) <= 1000
}
