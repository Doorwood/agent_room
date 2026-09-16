package workgroup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// Assignment is versioned and independent of any model vendor. Prior results
// are untrusted work products, never additional human instructions.
type Assignment struct {
	Workspace    *WorkspaceSnapshot `json:"workspace,omitempty"`
	StepID       string             `json:"stepId,omitempty"`
	StepPrompt   string             `json:"stepPrompt,omitempty"`
	DependsOn    []string           `json:"dependsOn,omitempty"`
	Version      int                `json:"version"`
	ID           string             `json:"assignmentId"`
	AgentID      string             `json:"agentId"`
	HumanUID     uint32             `json:"humanUid"`
	ProjectRoot  string             `json:"projectRoot"`
	TaskID       int64              `json:"taskId,omitempty"`
	HumanPrompt  string             `json:"humanPrompt,omitempty"`
	Prompt       string             `json:"prompt"`
	Instructions string             `json:"instructions,omitempty"`
	Prior        []Result           `json:"priorResults"`
}
type Result struct {
	WorkspacePath string            `json:"workspacePath,omitempty"`
	Changes       []WorkspaceChange `json:"-"`
	StepID        string            `json:"stepId,omitempty"`
	AgentID       string            `json:"agentId"`
	Text          string            `json:"text"`
}
type Provider interface {
	Run(context.Context, Member, Assignment) (Result, error)
}

const maxOutput = 64 << 10

// ExecProvider launches an administrator-installed adapter, without a shell.
// The adapter receives one JSON request and returns {"text":"..."} on stdout.
// It is trusted project code, not an OS sandbox.
type ExecProvider struct{}
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxOutput {
		return 0, fmt.Errorf("agent output exceeds 64 KiB")
	}
	return b.Buffer.Write(p)
}
func (ExecProvider) Run(ctx context.Context, m Member, a Assignment) (Result, error) {
	a.Prompt += a.stepInstructions()
	payload, e := json.Marshal(a)
	if e != nil {
		return Result{}, e
	}
	cmd := exec.CommandContext(ctx, m.Command[0], m.Command[1:]...)
	cmd.Dir = a.ProjectRoot
	cmd.Stdin = bytes.NewReader(payload)
	// Never inherit tokens, SSH agent sockets or arbitrary Host environment.
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 3 * time.Second
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	defer func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	if e = cmd.Run(); e != nil {
		return Result{}, fmt.Errorf("agent %s process failed or was cancelled", m.ID)
	}
	var r struct {
		Text string `json:"text"`
	}
	d := json.NewDecoder(&out)
	d.DisallowUnknownFields()
	if e = d.Decode(&r); e != nil {
		return Result{}, fmt.Errorf("agent %s returned invalid JSON", m.ID)
	}
	var extra any
	if d.Decode(&extra) != io.EOF || r.Text == "" {
		return Result{}, fmt.Errorf("agent %s returned invalid or empty result", m.ID)
	}
	return Result{AgentID: m.ID, Text: r.Text}, nil
}

// Explicit step scope supplements the original human request without replacing
// its authorization context. Legacy mention-only requests have no step prompt.
func (a Assignment) stepInstructions() string {
	if a.StepPrompt == "" {
		return ""
	}
	return "\n[当前协作步骤 " + a.StepID + "]\n只执行本步骤：" + a.StepPrompt + "\n其余步骤由工作组调度，不要自行执行或重新派发。\n"
}
