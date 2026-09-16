package workgroup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const WorkspaceLimit = 32 << 20
const WorkspaceFileLimit = 10000

type WorkspaceFile struct {
	Path       string `json:"path"`
	Data       []byte `json:"data"`
	Executable bool   `json:"executable,omitempty"`
}
type WorkspaceSnapshot struct {
	Files []WorkspaceFile `json:"files"`
}
type WorkspaceChange struct {
	File   WorkspaceFile `json:"file"`
	Before string        `json:"before"`
	Delete bool          `json:"delete,omitempty"`
}

func workspacePath(p string) bool {
	if p == "" || !fs.ValidPath(p) || strings.Contains(p, "\\") || strings.ContainsAny(p, "\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".git" || part == ".agent_room" || part == "node_modules" || part == ".ssh" || part == ".aws" || part == ".npmrc" || part == ".env" || strings.HasPrefix(part, ".env.") || part == "__pycache__" {
			return false
		}
	}
	return true
}
func fileHash(f WorkspaceFile) string {
	h := sha256.New()
	h.Write(f.Data)
	if f.Executable {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (s WorkspaceSnapshot) Validate() error {
	total := 0
	seen := map[string]bool{}
	if len(s.Files) > WorkspaceFileLimit {
		return fmt.Errorf("项目文件超过 %d 个", WorkspaceFileLimit)
	}
	for _, f := range s.Files {
		total += len(f.Data)
		if !workspacePath(f.Path) || seen[f.Path] || total > WorkspaceLimit {
			return fmt.Errorf("无效项目文件或快照超过 32 MiB：%s", f.Path)
		}
		seen[f.Path] = true
	}
	for p := range seen {
		for parent := filepath.ToSlash(filepath.Dir(p)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if seen[parent] {
				return fmt.Errorf("文件路径冲突：%s", p)
			}
		}
	}
	return nil
}

// SnapshotProject includes Git tracked and non-ignored untracked files. No Git
// metadata, credentials, links, dependencies or files outside the root travel.
func SnapshotProject(ctx context.Context, dir string, gitProject bool) (WorkspaceSnapshot, error) {
	var paths []string
	if gitProject {
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		var out cappedWorkspaceBuffer
		cmd.Stdout = &out
		if e := cmd.Run(); e != nil {
			return WorkspaceSnapshot{}, fmt.Errorf("Host 项目必须是可读取的 Git 工作区：%w", e)
		}
		for _, p := range strings.Split(out.String(), "\x00") {
			if workspacePath(p) {
				paths = append(paths, p)
			}
		}
	} else {
		e := filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if p == dir {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			rel = filepath.ToSlash(rel)
			if !workspacePath(rel) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("不支持符号链接：%s", rel)
			}
			if !d.IsDir() {
				paths = append(paths, rel)
			}
			if len(paths) > WorkspaceFileLimit {
				return fmt.Errorf("工作副本文件过多")
			}
			return nil
		})
		if e != nil {
			return WorkspaceSnapshot{}, e
		}
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		return WorkspaceSnapshot{}, e
	}
	defer root.Close()
	s := WorkspaceSnapshot{}
	seen := map[string]bool{}
	total := 0
	sort.Strings(paths)
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		st, e := root.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return s, e
		}
		if !st.Mode().IsRegular() {
			return s, fmt.Errorf("仅支持普通项目文件：%s", p)
		}
		for parent := filepath.Dir(p); parent != "."; parent = filepath.Dir(parent) {
			st, e := root.Lstat(parent)
			if e != nil || !st.IsDir() {
				return s, fmt.Errorf("不支持链接或异常父目录：%s", parent)
			}
		}
		f, e := root.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return s, e
		}
		opened, err := f.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(st, opened) {
			f.Close()
			return s, fmt.Errorf("快照读取期间文件变化：%s", p)
		}
		if stat, ok := opened.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			f.Close()
			return s, fmt.Errorf("不支持硬链接：%s", p)
		}
		data, e := io.ReadAll(io.LimitReader(f, int64(WorkspaceLimit-total+1)))
		finished, statErr := f.Stat()
		f.Close()
		if statErr != nil || finished.Size() != opened.Size() || !finished.ModTime().Equal(opened.ModTime()) {
			return s, fmt.Errorf("快照读取期间文件变化：%s", p)
		}
		if e != nil {
			return s, e
		}
		total += len(data)
		if total > WorkspaceLimit {
			return s, fmt.Errorf("项目快照超过 32 MiB，请排除大型构建产物")
		}
		s.Files = append(s.Files, WorkspaceFile{Path: p, Data: data, Executable: st.Mode().Perm()&0111 != 0})
	}
	return s, s.Validate()
}

type cappedWorkspaceBuffer struct{ bytes.Buffer }

func (b *cappedWorkspaceBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, fmt.Errorf("文件清单过大")
	}
	return b.Buffer.Write(p)
}

// MaterializeWorkspace only writes into a newly allocated private directory.
func MaterializeWorkspace(parent string, s WorkspaceSnapshot) (string, error) {
	if e := s.Validate(); e != nil {
		return "", e
	}
	if e := os.MkdirAll(parent, 0700); e != nil {
		return "", e
	}
	dir, e := os.MkdirTemp(parent, "workspace-")
	if e != nil {
		return "", e
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	for _, f := range s.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return "", e
		}
		mode := os.FileMode(0600)
		if f.Executable {
			mode = 0700
		}
		if e = os.WriteFile(p, f.Data, mode); e != nil {
			return "", e
		}
	}
	ok = true
	return dir, nil
}
func WorkspaceDiff(base, after WorkspaceSnapshot) []WorkspaceChange {
	before := map[string]WorkspaceFile{}
	for _, f := range base.Files {
		before[f.Path] = f
	}
	var changes []WorkspaceChange
	for _, f := range after.Files {
		old, ok := before[f.Path]
		if !ok {
			changes = append(changes, WorkspaceChange{File: f})
		} else if fileHash(old) != fileHash(f) {
			changes = append(changes, WorkspaceChange{File: f, Before: fileHash(old)})
		}
		delete(before, f.Path)
	}
	for _, f := range before {
		changes = append(changes, WorkspaceChange{File: WorkspaceFile{Path: f.Path}, Before: fileHash(f), Delete: true})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].File.Path < changes[j].File.Path })
	return changes
}

// ApplyWorkspaceChanges operates on memory only; never modifies the Host tree.
func ApplyWorkspaceChanges(base WorkspaceSnapshot, changes []WorkspaceChange) (WorkspaceSnapshot, error) {
	files := map[string]WorkspaceFile{}
	for _, f := range base.Files {
		files[f.Path] = f
	}
	seen := map[string]bool{}
	if len(changes) > WorkspaceFileLimit {
		return base, fmt.Errorf("变更文件过多")
	}
	for _, c := range changes {
		p := c.File.Path
		if !workspacePath(p) || seen[p] {
			return base, fmt.Errorf("无效或重复变更路径：%s", p)
		}
		seen[p] = true
		old, exists := files[p]
		hash := ""
		if exists {
			hash = fileHash(old)
		}
		if hash != c.Before {
			return base, fmt.Errorf("工作副本冲突：%s 的基线已变化", p)
		}
		if c.Delete {
			if !exists || len(c.File.Data) > 0 {
				return base, fmt.Errorf("无效删除：%s", p)
			}
			delete(files, p)
		} else {
			files[p] = c.File
		}
	}
	out := WorkspaceSnapshot{}
	for _, f := range files {
		out.Files = append(out.Files, f)
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out, out.Validate()
}
func saveWorkspaceReceipt(dir string, base WorkspaceSnapshot, changes []WorkspaceChange, worker, assignment string) error {
	raw, e := json.MarshalIndent(struct {
		Agent, Assignment string
		Base              WorkspaceSnapshot
		Changes           []WorkspaceChange
	}{worker, assignment, base, changes}, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-receipt.json"), raw, 0600)
}

// RunInWorkspace uses a fresh local mirror, never the member's existing project.
// Mirrors are retained for investigation if execution or delivery is uncertain.
func RunInWorkspace(ctx context.Context, m Member, a Assignment) (Result, error) {
	if a.Workspace == nil {
		return Result{}, fmt.Errorf("缺少 Host 项目快照")
	}
	cache, e := os.UserCacheDir()
	if e != nil {
		return Result{}, e
	}
	dir, e := MaterializeWorkspace(filepath.Join(cache, "agent_room", "workspaces"), *a.Workspace)
	if e != nil {
		return Result{}, e
	}
	base := *a.Workspace
	a.Workspace = nil
	a.ProjectRoot = dir
	a.Prompt += "\n当前目录是 Host 项目的独立副本，只在此目录工作。不要访问原 Host 路径或其他工作副本；不要执行 git commit/push。修改会回传 Host 独立目录，不会覆盖主项目。"
	result, e := (NativeProvider{Provider: m.Provider}).Run(ctx, m, a)
	if e != nil {
		return Result{}, e
	}
	after, e := SnapshotProject(ctx, dir, false)
	if e != nil {
		return Result{}, e
	}
	result.Changes = WorkspaceDiff(base, after)
	if m.Mode == "review" && len(result.Changes) > 0 {
		return Result{}, fmt.Errorf("只读 Agent 修改了工作副本，已拒绝回传")
	}
	return result, nil
}
