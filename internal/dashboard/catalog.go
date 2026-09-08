// Package dashboard manages only this process's local browser connections.
package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"agent_romm/internal/config"
	"agent_romm/internal/network"
)

type Room struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	Session     string `json:"session"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Project     string `json:"project,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	URL         string `json:"url,omitempty"`
}

func identity(address, session, name string) string {
	sum := sha256.Sum256([]byte(address + "\n" + session + "\n" + name))
	return hex.EncodeToString(sum[:])
}

type Catalog struct{ Home, Config string }

func (c Catalog) credentialDir() string { return filepath.Join(c.Config, "agent_room", "credentials") }
func privateJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return errors.New("invalid private metadata file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}
func (c Catalog) Credentials() ([]network.Credential, []string) {
	var result []network.Credential
	var warnings []string
	dir := c.credentialDir()
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return result, warnings
	}
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return result, []string{"成员凭证目录不可读取或权限不正确，请运行 agent_room doctor。"}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result, []string{"无法读取成员凭证目录。"}
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(result) >= 1024 {
			warnings = append(warnings, "仅显示前 1024 个成员身份。")
			break
		}
		var credential network.Credential
		err := privateJSON(filepath.Join(dir, entry.Name()), &credential)
		token, e := hex.DecodeString(credential.Token)
		_, _, s := network.ParseSession(credential.Session)
		_, a := network.Address(credential.Address)
		if err != nil || e != nil || len(token) != 32 || s != nil || a != nil || strings.TrimSpace(credential.Name) == "" || entry.Name() != identity(credential.Address, credential.Session, credential.Name)+".json" {
			warnings = append(warnings, "已跳过损坏或权限不正确的成员凭证；未修改原文件。")
			continue
		}
		result = append(result, credential)
	}
	return result, warnings
}

// RememberHost indexes explicitly located states without exposing private configuration.
func (c Catalog) RememberHost(state string) error {
	dir := filepath.Join(c.Config, "agent_room", "dashboard", "hosts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(state))
	target := filepath.Join(dir, fmt.Sprintf("%x.json", sum[:]))
	f, err := os.CreateTemp(dir, ".host-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = json.NewEncoder(f).Encode(struct {
		State string `json:"state"`
	}{state}); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}
func (c Catalog) owned() ([]Room, []string) {
	base := filepath.Join(c.Home, ".local", "share", "agent_room")
	states := map[string]bool{filepath.Join(base, "host"): true}
	for _, parent := range []string{"projects", "hosts"} {
		entries, _ := os.ReadDir(filepath.Join(base, parent))
		for _, entry := range entries {
			if entry.IsDir() {
				states[filepath.Join(base, parent, entry.Name())] = true
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(c.Config, "agent_room", "dashboard", "hosts", "*.json"))
	for _, path := range files {
		var saved struct{ State string }
		if privateJSON(path, &saved) == nil && filepath.IsAbs(saved.State) {
			states[saved.State] = true
		}
	}
	var rooms []Room
	var warnings []string
	for state := range states {
		cfg, err := config.Read(filepath.Join(state, "private", "config.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			warnings = append(warnings, "已跳过不可读取的本机 room 配置。")
			continue
		}
		var endpoint struct{ Address string }
		if privateJSON(filepath.Join(state, "private", "network.json"), &endpoint) != nil {
			continue
		}
		if _, err = network.Address(endpoint.Address); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(state, "private", "network.pem"))
		if err != nil {
			continue
		}
		cert, _ := pem.Decode(b)
		if cert == nil || cert.Type != "CERTIFICATE" {
			continue
		}
		sum := sha256.Sum256(cert.Bytes)
		session := string(cfg.RoomID) + "." + hex.EncodeToString(sum[:])
		if _, _, err = network.ParseSession(session); err != nil {
			continue
		}
		rooms = append(rooms, Room{ID: identity(endpoint.Address, session, ""), Address: endpoint.Address, Session: session, Kind: "owned", Project: cfg.ProjectRoot, Status: "disconnected"})
	}
	return rooms, warnings
}
func (c Catalog) List() ([]Room, []string) {
	owned, warnings := c.owned()
	creds, w := c.Credentials()
	warnings = append(warnings, w...)
	rooms := make([]Room, 0, len(owned)+len(creds))
	used := map[string]bool{}
	for _, cred := range creds {
		r := Room{ID: identity(cred.Address, cred.Session, cred.Name), Address: cred.Address, Session: cred.Session, Name: cred.Name, Kind: "joined", Status: "disconnected"}
		for _, host := range owned {
			if host.Address == r.Address && host.Session == r.Session {
				r.Kind = "owned"
				r.Project = host.Project
				used[host.ID] = true
			}
		}
		rooms = append(rooms, r)
	}
	for _, host := range owned {
		if !used[host.ID] {
			rooms = append(rooms, host)
		}
	}
	for i := range rooms {
		if rooms[i].Project == "" {
			var saved struct{ Project string }
			if privateJSON(c.projectFile(rooms[i].Address, rooms[i].Session), &saved) == nil {
				rooms[i].Project = saved.Project
			}
		}
		if rooms[i].Project != "" {
			rooms[i].ProjectName = projectName(rooms[i].Project)
		}
	}
	sort.Slice(rooms, func(i, j int) bool {
		if rooms[i].Kind != rooms[j].Kind {
			return rooms[i].Kind < rooms[j].Kind
		}
		return rooms[i].ID < rooms[j].ID
	})
	visible := rooms[:0]
	for _, r := range rooms {
		if !c.hidden(r.ID) {
			visible = append(visible, r)
		}
	}
	return visible, warnings
}
func (c Catalog) Add(address, session, name string) (Room, error) {
	address, err := network.Address(strings.TrimSpace(address))
	if err != nil {
		return Room{}, err
	}
	name = strings.TrimSpace(name)
	session = strings.TrimSpace(session)
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\x00\r\n\t") {
		return Room{}, errors.New("请输入 1–64 字节的昵称")
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return Room{}, errors.New("昵称不能包含控制字符")
		}
	}
	credentials, _ := c.Credentials()
	if len(credentials) >= 1024 {
		return Room{}, errors.New("本机成员身份已达到上限")
	}
	cred, err := network.CredentialFor(c.credentialDir(), address, session, name)
	if err != nil {
		return Room{}, err
	}
	if err := os.Remove(c.removedFile(identity(address, session, name))); err != nil && !os.IsNotExist(err) {
		return Room{}, err
	}
	return Room{ID: identity(address, session, name), Address: address, Session: session, Name: cred.Name, Kind: "joined", Status: "disconnected"}, nil
}

func projectName(project string) string {
	return filepath.Base(strings.ReplaceAll(strings.TrimRight(project, "/\\"), "\\", "/"))
}
func (c Catalog) projectFile(address, session string) string {
	return filepath.Join(c.Config, "agent_room", "dashboard", "projects", identity(address, session, "")+".json")
}

// RememberProject caches metadata received from an authenticated host, shared by room identities.
func (c Catalog) RememberProject(address, session, project string) error {
	if strings.TrimSpace(project) == "" || len(project) > 4096 {
		return errors.New("invalid project metadata")
	}
	target := c.projectFile(address, session)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".project-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(struct {
		Project string `json:"project"`
	}{project}); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}

func (c Catalog) removedFile(id string) string {
	return filepath.Join(c.Config, "agent_room", "dashboard", "removed", id+".json")
}
func (c Catalog) hidden(id string) bool {
	var removed bool
	return privateJSON(c.removedFile(id), &removed) == nil && removed
}

// Remove hides this local entry, preserving membership credentials and host data.
func (c Catalog) Remove(id string) error {
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 32 {
		return errors.New("invalid room ID")
	}
	file := c.removedFile(id)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	return os.WriteFile(file, []byte("true"), 0600)
}
