package dashboard

import (
	"agent_romm/internal/network"
	"agent_romm/internal/workgroup"
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

// checkAgent keeps the lease alive while probing; the Host has already disabled
// dispatch. Failures remain visible and require an explicit retry.
func (s *Server) checkAgent(ctx context.Context, l network.Launcher, a *localAgent) bool {
	for {
		s.agentStatus(a, "checking", "正在验证模型响应和工具权限（最长 90 秒）")
		started := time.Now()
		probe, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			if a.WorkspaceScope == "local" {
				dir, e := os.Open(a.Project)
				if e != nil {
					done <- fmt.Errorf("项目目录检查失败: %w", e)
					return
				}
				_, e = dir.Readdirnames(1)
				dir.Close()
				if e != nil && e != io.EOF {
					done <- fmt.Errorf("项目目录不可读: %w", e)
					return
				}
			}
			done <- workgroup.HealthCheck(probe, a.Provider, a.Mode)
		}()
		ticker := time.NewTicker(2 * time.Second)
		var err error
	checking:
		for {
			select {
			case err = <-done:
				break checking
			case <-ctx.Done():
				cancel()
				<-done
				err = ctx.Err()
				break checking
			case <-ticker.C:
				_, e := l.Worker(ctx, workgroup.WorkerRequest{Action: "poll", ID: a.WorkerID})
				if e != nil {
					cancel()
					<-done
					err = fmt.Errorf("Host 连接检查失败: %w", e)
					break checking
				}
			}
		}
		ticker.Stop()
		cancel()
		s.mu.Lock()
		a.CheckMillis = time.Since(started).Milliseconds()
		s.mu.Unlock()
		if ctx.Err() != nil {
			return false
		}
		if err == nil {
			reply, e := l.Worker(ctx, workgroup.WorkerRequest{Action: "health", ID: a.WorkerID, Healthy: true})
			if e != nil {
				s.agentStatus(a, "error", "健康回执未确认，请重新邀请："+e.Error())
				return false
			} else if reply.State != "health-accepted" {
				err = fmt.Errorf("Host 未确认健康状态，请更新 Host")
			}
		}
		if err == nil {
			s.mu.Lock()
			a.LastHealthy = time.Now()
			s.mu.Unlock()
			s.agentStatus(a, "idle", "")
			return true
		}
		s.agentStatus(a, "error", err.Error())
		// Heartbeats alone never recover a failed model check.
		retry := time.NewTicker(2 * time.Second)
	waiting:
		for {
			select {
			case <-ctx.Done():
				retry.Stop()
				return false
			case <-a.recheck:
				break waiting
			case <-retry.C:
				if _, e := l.Worker(ctx, workgroup.WorkerRequest{Action: "poll", ID: a.WorkerID}); e != nil {
					retry.Stop()
					s.agentStatus(a, "error", "连接已断开，请移除后重新邀请 Agent")
					return false
				}
			}
		}
		retry.Stop()
	}
}
