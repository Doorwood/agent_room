package resources

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendTargetValidation(t *testing.T) {
	for _, target := range []string{"https://bytedance.sg.larkoffice.com/docx/BP2edVezXoSUNMx6KEjlXCUigHc", "https://example.feishu.cn/docx/Abcdef123456"} {
		if !ValidAppendTarget(target) {
			t.Fatal(target)
		}
	}
	for _, target := range []string{"Abcdef123456", "https://evil.com/docx/Abcdef123456", "https://example.feishu.cn/wiki/Abcdef123456", "https://example.feishu.cn/docx/Abcdef123456?x=1", "https://x@feishu.cn/docx/Abcdef123456", "https://feishu.cn:443/docx/Abcdef123456"} {
		if ValidAppendTarget(target) {
			t.Fatal(target)
		}
	}
}
func TestAppendVerifiedIdempotentAndUnknown(t *testing.T) {
	for _, scenario := range []string{"success", "empty", "partial", "denied", "unreadable", "already-present", "wrong-account"} {
		t.Run(scenario, func(t *testing.T) {
			r := createRequest()
			r.Action = "feishu.append"
			r.Target = "https://example.feishu.cn/docx/Abcdef123456"
			writes := 0
			body := "<title>existing</title><p>keep me</p>"
			if scenario == "already-present" {
				body += paragraphXML(r.Content)
			}
			e := Executor{ReceiptDir: filepath.Join(t.TempDir(), "receipts"), command: func(ctx context.Context, args []string, input string) ([]byte, error) {
				switch args[0] + " " + args[1] {
				case "whoami --as":
					account := r.AccountID
					if scenario == "wrong-account" {
						account = "ou_someoneelse123"
					}
					return json.Marshal(map[string]any{"identity": "user", "available": true, "tokenStatus": "ready", "onBehalfOf": map[string]string{"openId": account}})
				case "docs +fetch":
					if scenario == "unreadable" {
						return nil, errors.New("secret denied")
					}
					return json.Marshal(map[string]any{"ok": true, "identity": "user", "data": map[string]any{"document": map[string]string{"content": body}}})
				case "docs +update":
					writes++
					if !strings.Contains(strings.Join(args, " "), "--doc "+r.Target+" --command append") || input != paragraphXML(r.Content) {
						t.Fatal(args, input)
					}
					if scenario == "success" {
						body += input
					}
					if scenario == "partial" {
						body += "<p>正文 &amp; 内容</p>"
					}
					if scenario == "denied" {
						return nil, errors.New("secret denied")
					}
					return []byte(`{"ok":true,"identity":"user"}`), nil
				default:
					t.Fatalf("unexpected command %v", args)
					return nil, nil
				}
			}}
			result, err := e.Run(context.Background(), r)
			if scenario == "wrong-account" {
				if err == nil || writes != 0 {
					t.Fatal(result, err, writes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var receipt CreateReceipt
			json.Unmarshal([]byte(result), &receipt)
			expected := "unknown"
			if scenario == "success" || scenario == "already-present" {
				expected = "completed"
			}
			if receipt.State != expected || receipt.Target != r.Target || receipt.ContentSHA256 != ContentDigest(r.Content) || strings.Contains(result, "secret") {
				t.Fatal(result)
			}
			before := writes
			again, err := e.Run(context.Background(), r)
			if err != nil || again != result || writes != before {
				t.Fatal("replayed", again, err)
			}
			if (scenario == "unreadable" || scenario == "already-present") && writes != 0 {
				t.Fatal(writes)
			}
		})
	}
}
func TestCreateEmptyDocumentIsNotCompleted(t *testing.T) {
	r := createRequest()
	e := Executor{ReceiptDir: filepath.Join(t.TempDir(), "receipts"), command: func(ctx context.Context, args []string, input string) ([]byte, error) {
		if args[0] == "whoami" {
			return []byte(`{"identity":"user","available":true,"tokenStatus":"ready","onBehalfOf":{"openId":"ou_alice123456"}}`), nil
		}
		if args[1] == "+fetch" {
			return []byte(`{"ok":true,"identity":"user","data":{"document":{"content":"<title>title</title>"}}}`), nil
		}
		return []byte(`{"ok":true,"identity":"user","data":{"document":{"document_id":"Abcdef123456","url":"https://example.feishu.cn/docx/Abcdef123456"}}}`), nil
	}}
	got, err := e.Run(context.Background(), r)
	if err != nil || !strings.Contains(got, `"state":"unknown"`) {
		t.Fatal(got, err)
	}
}
