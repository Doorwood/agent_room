package answerwindow

import (
	"agent_romm/internal/network"
	"agent_romm/internal/personal"
	"agent_romm/internal/resources"
	"context"
	"strings"
	"testing"
)

func TestAppendDoesNotReuseCreateConsent(t *testing.T) {
	w := &Window{personalClient: strings.Repeat("c", 32), personalAccount: "ou_alice123456"}
	p := &personal.Request{ID: strings.Repeat("a", 32), Action: "feishu.append", Target: "https://example.feishu.cn/docx/Abcdef123456", Title: "补写", Content: "正文"}
	calls := 0
	w.resource = func(_ context.Context, r network.ResourceRequest, _ resources.Execute) (network.ResourceReply, error) {
		calls++
		if r.Action != "personal-pending" {
			t.Fatal("executed without consent")
		}
		return network.ResourceReply{Mode: "personal", Role: "roommate", Personal: p}, nil
	}
	w.personalStep(context.Background())
	if calls != 1 || w.personalRunCancel != nil {
		t.Fatal("create consent reused")
	}
	w.personalAppendGranted = strings.Repeat("b", 32)
	w.personalStep(context.Background())
	if calls != 2 || w.personalRunCancel != nil {
		t.Fatal("other request consent reused")
	}
}
