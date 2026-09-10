package cli

import (
	"agent_romm/internal/gitpush"
	"agent_romm/internal/network"
	"agent_romm/internal/personal"
	"agent_romm/internal/resources"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func runPersonalCreate(ctx context.Context, args []string, out, diag io.Writer, d Dependencies) int {
	return runPersonalDocument(ctx, args, out, diag, d, false)
}
func runPersonalAppend(ctx context.Context, args []string, out, diag io.Writer, d Dependencies) int {
	return runPersonalDocument(ctx, args, out, diag, d, true)
}
func runPersonalDocument(ctx context.Context, args []string, out, diag io.Writer, d Dependencies, appendDoc bool) int {
	fs := flag.NewFlagSet("personal-document", flag.ContinueOnError)
	fs.SetOutput(diag)
	var state, capability string
	fs.StringVar(&state, "state", "", "Host state directory")
	fs.StringVar(&capability, "capability", "", "current sender capability")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !filepath.IsAbs(state) || len(capability) != 64 || d.Input == nil {
		fmt.Fprintln(diag, "需要当前工作轮次绑定的个人创建命令及 stdin JSON；不得使用 Host 飞书账号代建")
		return 2
	}
	var content struct {
		Target  string `json:"target,omitempty"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	data, err := io.ReadAll(io.LimitReader(d.Input, 196609))
	if err != nil || len(data) > 196608 {
		fmt.Fprintln(diag, "文档输入过大或无法读取")
		return 2
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&content) != nil || dec.Decode(new(any)) != io.EOF {
		fmt.Fprintln(diag, "stdin 必须是包含 title 和 content 的 JSON")
		return 2
	}
	if (appendDoc && !resources.ValidAppendTarget(content.Target)) || (!appendDoc && content.Target != "") {
		fmt.Fprintln(diag, "需要明确的 https 飞书 docx 链接")
		return 2
	}
	body, _ := json.Marshal(map[string]string{"capability": capability, "title": content.Title, "content": content.Content, "target": content.Target})
	if appendDoc {
		if err := network.PersonalAppend(ctx, filepath.Join(state, "private"), bytes.NewReader(body), out); err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
		return 0
	}
	if err := network.PersonalCreate(ctx, filepath.Join(state, "private"), bytes.NewReader(body), out); err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	return 0
}

func runPersonalCommit(ctx context.Context, args []string, out, diag io.Writer, d Dependencies) int {
	fs := flag.NewFlagSet("personal-commit", flag.ContinueOnError)
	fs.SetOutput(diag)
	var state, capability string
	fs.StringVar(&state, "state", "", "Host state directory")
	fs.StringVar(&capability, "capability", "", "current sender capability")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !filepath.IsAbs(state) || len(capability) != 64 || d.Input == nil {
		return 2
	}
	data, err := io.ReadAll(io.LimitReader(d.Input, 65537))
	if err != nil || len(data) > 65536 {
		fmt.Fprintln(diag, "提交请求过大")
		return 2
	}
	var in personal.CommitInput
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || in.Validate() != nil {
		fmt.Fprintln(diag, "stdin 需要 message、paths、expectedHead，不能指定其他人的 Git 身份")
		return 2
	}
	body, _ := json.Marshal(map[string]any{"capability": capability, "commit": in})
	if err := network.PersonalCommit(ctx, filepath.Join(state, "private"), bytes.NewReader(body), out); err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	return 0
}

func runPersonalPush(ctx context.Context, args []string, out, diag io.Writer, d Dependencies) int {
	fs := flag.NewFlagSet("personal-push", flag.ContinueOnError)
	fs.SetOutput(diag)
	var state, capability string
	fs.StringVar(&state, "state", "", "Host state directory")
	fs.StringVar(&capability, "capability", "", "current sender capability")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !filepath.IsAbs(state) || len(capability) != 64 || d.Input == nil {
		return 2
	}
	var in gitpush.Input
	data, err := io.ReadAll(io.LimitReader(d.Input, 8193))
	if err != nil || len(data) > 8192 {
		return 2
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || in.Validate() != nil {
		fmt.Fprintln(diag, "stdin 只接受 repository、branch 和 commit，不接受账号、令牌或命令")
		return 2
	}
	body, _ := json.Marshal(map[string]any{"capability": capability, "push": in})
	if err := network.PersonalPush(ctx, filepath.Join(state, "private"), bytes.NewReader(body), out); err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	return 0
}
func runPersonalGitCredential(args []string, out io.Writer, d Dependencies) int {
	if len(args) != 1 || d.Input == nil {
		return 2
	}
	if err := gitpush.Credential(d.Input, out, args[0], os.Getenv("AGENT_ROOM_PUSH_TOKEN"), os.Getenv("AGENT_ROOM_PUSH_REPOSITORY")); err != nil {
		return 1
	}
	return 0
}
