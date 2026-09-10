package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestPersonalCreateAcceptsOnlyTypedStdinAndCurrentCapability(t *testing.T) {
	for _, body := range []string{`{"title":"Doc","content":"body","uid":123}`, `{"title":"Doc","content":"body","token":"secret"}`, `{"title":"Doc","content":"body"} {}`, strings.Repeat(" ", 196609)} {
		var out, diag bytes.Buffer
		code := runPersonalCreate(context.Background(), []string{"--state", "/tmp/unused-personal-state", "--capability", strings.Repeat("a", 64)}, &out, &diag, Dependencies{Input: io.NopCloser(strings.NewReader(body))})
		if code != 2 || out.Len() != 0 || strings.Contains(diag.String(), "secret") {
			t.Fatal(code, out.String(), diag.String())
		}
	}
}

func TestPersonalCommitCannotSupplyAuthorOrRepository(t *testing.T) {
	for _, body := range []string{`{"message":"test","paths":["file.go"],"expectedHead":"unborn","author":{"name":"Host","email":"host@example.test"}}`, `{"message":"test","paths":["file.go"],"expectedHead":"unborn","root":"/another/project"}`, `{"message":"test","paths":["../escape"],"expectedHead":"unborn"}`} {
		var out, diag bytes.Buffer
		code := runPersonalCommit(context.Background(), []string{"--state", "/tmp/unused-personal-state", "--capability", strings.Repeat("a", 64)}, &out, &diag, Dependencies{Input: io.NopCloser(strings.NewReader(body))})
		if code != 2 || out.Len() != 0 {
			t.Fatal(code, out.String(), diag.String())
		}
	}
}

func TestPersonalPushRejectsCredentialsForceAndAlternateHosts(t *testing.T) {
	for _, body := range []string{
		`{"repository":"owner/repo","branch":"main","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","token":"secret"}`,
		`{"repository":"owner/repo","branch":"main","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","force":true}`,
		`{"repository":"https://evil.test/owner/repo","branch":"main","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		`{"repository":"owner/repo","branch":"main","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {}`,
		strings.Repeat(" ", 8193),
	} {
		var out, diag bytes.Buffer
		code := runPersonalPush(context.Background(), []string{"--state", "/tmp/unused-personal-state", "--capability", strings.Repeat("a", 64)}, &out, &diag, Dependencies{Input: io.NopCloser(strings.NewReader(body))})
		if code != 2 || out.Len() != 0 || strings.Contains(diag.String(), "secret") {
			t.Fatal(code, out.String(), diag.String())
		}
	}
}

func TestPersonalAppendRejectsUnscopedOrDestructiveInput(t *testing.T) {
	for _, body := range []string{
		`{"title":"Append","content":"body"}`,
		`{"title":"Append","content":"body","target":"https://evil.test/docx/Abcdef123456"}`,
		`{"title":"Append","content":"body","target":"https://example.feishu.cn/docx/Abcdef123456","mode":"overwrite"}`,
		`{"title":"Append","content":"body","target":"https://example.feishu.cn/docx/Abcdef123456","accountId":"ou_someoneelse"}`,
		`{"title":"Append","content":"body","target":"https://example.feishu.cn/docx/Abcdef123456","token":"secret"}`,
	} {
		var out, diag bytes.Buffer
		code := runPersonalAppend(context.Background(), []string{"--state", "/tmp/unused-personal-state", "--capability", strings.Repeat("a", 64)}, &out, &diag, Dependencies{Input: io.NopCloser(strings.NewReader(body))})
		if code != 2 || out.Len() != 0 || strings.Contains(diag.String(), "secret") {
			t.Fatal(code, out.String(), diag.String())
		}
	}
}
