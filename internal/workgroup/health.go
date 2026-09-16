package workgroup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// HealthCheck uses the production provider path in a disposable fixture. The
// challenge is only in the file, so echoing the prompt cannot pass the check.
func HealthCheck(ctx context.Context, provider, mode string) error {
	ctx, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	if mode != "review" && mode != "work" {
		return fmt.Errorf("无效的 Agent 权限模式")
	}
	path := ""
	for _, installed := range Detect() {
		if installed.Provider == provider && installed.Installed {
			path = installed.Path
		}
	}
	if path == "" {
		return fmt.Errorf("本机未安装 %s CLI", provider)
	}
	version, cancel := context.WithTimeout(ctx, 8*time.Second)
	cmd := exec.CommandContext(version, path, "--version")
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	cancel()
	if err != nil {
		return fmt.Errorf("CLI 版本/启动检查失败: %w", err)
	}
	return checkHealth(ctx, provider, mode, (NativeProvider{Provider: provider}).Run)
}
func checkHealth(ctx context.Context, provider, mode string, run func(context.Context, Member, Assignment) (Result, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "agent-room-health-")
	if err != nil {
		return fmt.Errorf("创建检查目录失败: %w", err)
	}
	defer os.RemoveAll(root)
	var nonce [24]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(nonce[:])
	if err = os.WriteFile(filepath.Join(root, "challenge.txt"), []byte(token), 0600); err != nil {
		return err
	}
	prompt := "Health check: read challenge.txt and reply with its exact contents only. Do not use network tools or modify any files."
	if mode == "work" {
		prompt = "Health check: read challenge.txt, write its exact contents to result.txt, then reply with those exact contents only. Do not modify other files or use network tools."
	}
	result, err := run(ctx, Member{ID: "health-check", Provider: provider, Mode: mode}, Assignment{ProjectRoot: root, Prompt: prompt})
	if err != nil {
		return fmt.Errorf("模型执行检查失败: %w", err)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("检查超时: %w", ctx.Err())
	}
	if strings.TrimSpace(result.Text) != token {
		return fmt.Errorf("模型未返回正确的文件口令，请检查工具读取能力")
	}
	original, err := os.ReadFile(filepath.Join(root, "challenge.txt"))
	if err != nil || string(original) != token {
		return fmt.Errorf("检查源文件被修改")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "challenge.txt" && !(mode == "work" && entry.Name() == "result.txt") {
			return fmt.Errorf("模型修改了检查范围以外的文件")
		}
	}
	if mode == "work" {
		st, e := os.Lstat(filepath.Join(root, "result.txt"))
		if e != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("编辑能力检查失败：未生成普通结果文件")
		}
		b, e := os.ReadFile(filepath.Join(root, "result.txt"))
		if e != nil || string(b) != token {
			return fmt.Errorf("编辑能力检查失败：文件内容不匹配")
		}
	}
	return nil
}
