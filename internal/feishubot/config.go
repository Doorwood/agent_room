// Package feishubot bridges an explicitly configured Lark bot to one project.
package feishubot

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var openID = regexp.MustCompile(`^ou_[A-Za-z0-9_-]{8,100}$`)
var chatID = regexp.MustCompile(`^oc_[A-Za-z0-9_-]{8,100}$`)
var messageID = regexp.MustCompile(`^om_[A-Za-z0-9_-]{8,150}$`)

type Binding struct {
	OpenID    string `json:"openId"`
	UID       uint32 `json:"roomUID"`
	AllowWork bool   `json:"allowWork"`
	// Empty means private messages only. Group triggers and readable history
	// require separate explicit grants; responses are always sent privately.
	OriginChats   []string `json:"originChats,omitempty"`
	ReadableChats []string `json:"readableChats,omitempty"`
}
type Config struct {
	RoomID   string    `json:"roomId"`
	Profile  string    `json:"profile"`
	AppID    string    `json:"appId"`
	Name     string    `json:"name"`
	Bindings []Binding `json:"bindings"`
}

func Load(path string) (Config, error) {
	var c Config
	st, err := os.Lstat(path)
	if err != nil {
		return c, err
	}
	if !filepath.IsAbs(path) || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 65536 {
		return c, errors.New("机器人配置必须是绝对路径、私有普通文件（0600，最大 64 KiB）")
	}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, errors.New("invalid bot config")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(c.RoomID) {
		return errors.New("explicit Room ID required")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`).MatchString(c.Profile) {
		return errors.New("explicit lark-cli profile required")
	}
	if !regexp.MustCompile(`^cli_[A-Za-z0-9]{8,100}$`).MatchString(c.AppID) || c.Name == "" || len(c.Name) > 80 || len(c.Bindings) == 0 || len(c.Bindings) > 100 {
		return errors.New("invalid bot app, name or bindings")
	}
	seen := map[string]bool{}
	uids := map[uint32]bool{}
	for _, b := range c.Bindings {
		if !openID.MatchString(b.OpenID) || b.UID == 0 || seen[b.OpenID] || uids[b.UID] {
			return errors.New("bot bindings require unique open IDs and Room UIDs")
		}
		seen[b.OpenID] = true
		uids[b.UID] = true
		for _, ch := range append(append([]string{}, b.OriginChats...), b.ReadableChats...) {
			if !chatID.MatchString(ch) {
				return errors.New("invalid allowed chat")
			}
		}
	}
	return nil
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
