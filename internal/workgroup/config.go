// Package workgroup adapts heterogeneous workers to Room's durable execution
// queue. Humans own assignments and approval; workers only produce results.
package workgroup

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Member struct {
	Mode           string   `json:"mode,omitempty"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Provider       string   `json:"provider"`
	Description    string   `json:"description,omitempty"`
	Instructions   string   `json:"instructions,omitempty"`
	Command        []string `json:"command,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}
type Config struct {
	Version int      `json:"version"`
	Members []Member `json:"members"`
}

var validID = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,31}$`)

func Default() Config {
	return Config{Version: 1, Members: []Member{{ID: "codex", Name: "Codex Agent", Provider: "codex", TimeoutSeconds: 1800}}}
}
func Load(path string) (Config, error) {
	c := Default()
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return c, e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 65536 {
		return c, fmt.Errorf("agents.json must be a private regular file (chmod 600), at most 64 KiB")
	}
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, fmt.Errorf("unexpected trailing agent configuration")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.Version != 1 || len(c.Members) < 1 || len(c.Members) > 16 {
		return fmt.Errorf("agent config requires version 1 and 1–16 members")
	}
	seen := map[string]bool{}
	builtin := false
	for _, m := range c.Members {
		if !validID.MatchString(m.ID) || seen[m.ID] || strings.TrimSpace(m.Name) == "" || len(m.Name) > 120 || len(m.Description) > 500 || len(m.Instructions) > 8000 || m.TimeoutSeconds < 0 || m.TimeoutSeconds > 7200 {
			return fmt.Errorf("invalid or duplicate agent member %q", m.ID)
		}
		seen[m.ID] = true
		if m.ID == "codex" {
			builtin = m.Provider == "codex"
		}
		switch m.Provider {
		case "codex":
			if len(m.Command) != 0 {
				return fmt.Errorf("codex member cannot specify command")
			}
		case "cursor", "claude-code":
			if m.Mode != "" && m.Mode != "review" && m.Mode != "work" {
				return fmt.Errorf("invalid native agent mode")
			}
			if len(m.Command) > 1 || (len(m.Command) == 1 && !filepath.IsAbs(m.Command[0])) {
				return fmt.Errorf("native agent command must be an absolute executable path")
			}
		case "exec":
			if len(m.Command) == 0 || len(m.Command) > 32 || !filepath.IsAbs(m.Command[0]) {
				return fmt.Errorf("exec member requires an absolute executable and argument array")
			}
		default:
			if !validID.MatchString(m.Provider) {
				return fmt.Errorf("invalid provider %q", m.Provider)
			}
		}
	}
	if !builtin {
		return fmt.Errorf("the default codex member is required")
	}
	return nil
}

// Route only interprets leading @agent:<stable-id> tokens in the original human
// message. Mentions inside quotes, task output or authorization text never route.
func (c Config) Route(text string) ([]Member, error) {
	s := strings.TrimSpace(text)
	var out []Member
	seen := map[string]bool{}
	for strings.HasPrefix(s, "@agent:") {
		token, rest, _ := strings.Cut(s, " ") // accept all whitespace, including newlines
		if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
			token, rest = s[:i], s[i:]
		} else {
			token, rest = s, ""
		}
		id := strings.TrimPrefix(token, "@agent:")
		var found *Member
		for i := range c.Members {
			if c.Members[i].ID == id {
				found = &c.Members[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("unknown agent @agent:%s", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate agent @agent:%s", id)
		}
		seen[id] = true
		out = append(out, *found)
		s = strings.TrimSpace(rest)
	}
	if len(out) > 0 && s == "" {
		return nil, fmt.Errorf("请在 @Agent 后填写工作内容")
	}
	if len(out) > 8 {
		return nil, fmt.Errorf("一次最多安排 8 位 Agent")
	}
	return out, nil
}

// Save persists the human administrator's directory; chat messages cannot edit it.
func Save(path string, c Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	raw, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".agents-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = f.Write(raw); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
