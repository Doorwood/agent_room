package workgroup

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Step identifies a unit of work, independently from the member running it.
// Dependencies refer to step IDs, allowing one member to work at multiple stages.
type Step struct {
	ID        string   `json:"id"`
	Agent     string   `json:"agent"`
	Prompt    string   `json:"prompt"`
	DependsOn []string `json:"dependsOn,omitempty"`
}
type Plan struct {
	Steps []Step `json:"steps"`
}
type plannedStep struct {
	Step
	Member Member
}
type executionPlan struct {
	Automatic bool
	Explicit  bool
	Steps     []plannedStep
}
type stepReceipt struct {
	Step
	State string `json:"state"`
}

// Plan selects explicit JSON plans or natural language planning. Only human
// submissions can initiate planning; worker outputs never route new assignments.
func (c Config) Plan(text string) (executionPlan, error) {
	s := strings.TrimSpace(text)
	if s != "/team" && !strings.HasPrefix(s, "/team ") && !strings.HasPrefix(s, "/team\n") && !strings.HasPrefix(s, "/team\t") && !strings.HasPrefix(s, "/team\r") {
		members, err := c.Route(text)
		if err != nil {
			return executionPlan{}, err
		}
		// A single explicitly selected member is direct dispatch, even when
		// the requested work discusses collaboration. No planning prerequisite.
		if len(members) == 1 {
			m := members[0]
			return executionPlan{Steps: []plannedStep{{Step: Step{ID: "direct", Agent: m.ID}, Member: m}}}, nil
		}
		return executionPlan{Automatic: len(members) > 1 || c.cooperationRequested(s)}, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(s, "/team")), "{") {
		if strings.TrimSpace(strings.TrimPrefix(s, "/team")) == "" {
			return executionPlan{}, fmt.Errorf("请描述协作目标")
		}
		return executionPlan{Automatic: true}, nil
	}
	p := executionPlan{Explicit: true}
	if len(s) > 65536 {
		return p, fmt.Errorf("协作计划不能超过 64 KiB")
	}
	var raw Plan
	d := json.NewDecoder(strings.NewReader(strings.TrimSpace(strings.TrimPrefix(s, "/team"))))
	d.DisallowUnknownFields()
	if err := d.Decode(&raw); err != nil {
		return p, fmt.Errorf("/team 需要 JSON 协作计划：%w", err)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, fmt.Errorf("协作计划后不能追加其他内容")
	}
	if len(raw.Steps) < 1 || len(raw.Steps) > 16 {
		return p, fmt.Errorf("协作计划需要 1–16 个步骤")
	}
	members := map[string]Member{}
	for _, m := range c.Members {
		members[m.ID] = m
	}
	steps := map[string]Step{}
	for _, step := range raw.Steps {
		if !validID.MatchString(step.ID) || steps[step.ID].ID != "" {
			return p, fmt.Errorf("无效或重复的步骤 ID：%s", step.ID)
		}
		if _, ok := members[step.Agent]; !ok {
			return p, fmt.Errorf("未知 Agent：%s", step.Agent)
		}
		if strings.TrimSpace(step.Prompt) == "" || len(step.Prompt) > 8000 {
			return p, fmt.Errorf("步骤 %s 需要工作内容，且不超过 8000 字节", step.ID)
		}
		steps[step.ID] = step
	}
	for _, step := range raw.Steps {
		seen := map[string]bool{}
		for _, dep := range step.DependsOn {
			if dep == step.ID || steps[dep].ID == "" || seen[dep] {
				return p, fmt.Errorf("步骤 %s 的依赖无效：%s", step.ID, dep)
			}
			seen[dep] = true
		}
	}
	// Stable topological ordering: the first ready step in the authored plan wins.
	done := map[string]bool{}
	for len(p.Steps) < len(raw.Steps) {
		found := false
		for _, step := range raw.Steps {
			if done[step.ID] {
				continue
			}
			ready := true
			for _, dep := range step.DependsOn {
				if !done[dep] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			p.Steps = append(p.Steps, plannedStep{Step: step, Member: members[step.Agent]})
			done[step.ID] = true
			found = true
			break
		}
		if !found {
			return p, fmt.Errorf("协作计划存在循环依赖")
		}
	}
	return p, nil
}

// Natural collaboration is routed from the human message; dependency semantics
// are inferred by the planner, never by keyword ordering.
func (c Config) cooperationRequested(text string) bool {
	if strings.Contains(text, "@agent:") {
		return true
	}
	count := 0
	for _, m := range c.Members {
		boundary := regexp.MustCompile(`(^|[^a-zA-Z0-9_.-])` + regexp.QuoteMeta(m.ID) + `($|[^a-zA-Z0-9_.-])`)
		if boundary.MatchString(text) || (m.Name != m.ID && strings.Contains(text, m.Name)) {
			count++
		}
	}
	return (len(c.Members) > 1 && (strings.Contains(text, "协作") || strings.Contains(text, "配合"))) || count >= 2 || strings.Contains(text, "多个agent") || strings.Contains(text, "多个 Agent") || strings.Contains(text, "多 Agent") || strings.Contains(text, "多agent") || strings.Contains(text, "多智能体")
}
