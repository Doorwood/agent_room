package resources

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func createRequest() Request {
	return Request{Action: "feishu.create", Title: "测试 <title>", Content: "正文 & 内容\n<img src=\"/private/file\"/>", RequestID: strings.Repeat("a", 32), AccountID: "ou_alice123456"}
}
func fakeCreator(t *testing.T, dir, account string, calls *atomic.Int32, fail bool) Executor {
	t.Helper()
	return Executor{ReceiptDir: dir, command: func(ctx context.Context, args []string, input string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"whoami", "--as", "user"}) {
			b, _ := json.Marshal(map[string]any{"identity": "user", "available": true, "tokenStatus": "ready", "onBehalfOf": map[string]string{"userName": "Test User", "openId": account}, "secret": "never-forward-this"})
			return b, nil
		}
		if len(args) > 1 && args[1] == "+fetch" {
			b, _ := json.Marshal(map[string]any{"ok": true, "identity": "user", "data": map[string]any{"document": map[string]string{"content": documentXML(createRequest().Title, createRequest().Content)}}})
			return b, nil
		}
		if !reflect.DeepEqual(args, []string{"docs", "+create", "--as", "user", "--doc-format", "xml", "--content", "-", "--json"}) {
			t.Errorf("unexpected command: %v", args)
		}
		calls.Add(1)
		if strings.Contains(input, "<img") || !strings.Contains(input, "&lt;img") || !strings.Contains(input, "&amp;") {
			t.Errorf("content interpreted as markup: %s", input)
		}
		if fail {
			return []byte(`{"secret":"never-forward-this"}`), errors.New("timeout secret")
		}
		return []byte(`{"ok":true,"identity":"user","secret":"never-forward-this","data":{"document":{"document_id":"Abcdef123456","url":"https://example.feishu.cn/docx/Abcdef123456"}}}`), nil
	}}
}
func TestCreateUsesLocalUserAndDurableReceipt(t *testing.T) {
	ctx := context.Background()
	r := createRequest()
	var calls atomic.Int32
	dir := filepath.Join(t.TempDir(), "receipts")
	e := fakeCreator(t, dir, r.AccountID, &calls, false)
	text, err := e.Run(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	var receipt CreateReceipt
	if json.Unmarshal([]byte(text), &receipt) != nil || receipt.State != "completed" || receipt.Account.OpenID != r.AccountID || receipt.URL == "" || strings.Contains(text, "secret") {
		t.Fatal(text)
	}
	// A restarted client reads its receipt without even invoking the CLI again.
	restarted := Executor{ReceiptDir: dir, command: func(context.Context, []string, string) ([]byte, error) {
		t.Error("replayed write")
		return nil, errors.New("unexpected")
	}}
	again, err := restarted.Run(ctx, r)
	if err != nil || again != text || calls.Load() != 1 {
		t.Fatal(again, err, calls.Load())
	}
	r.Title = "changed"
	if _, err = restarted.Run(ctx, r); err == nil {
		t.Fatal("request ID accepted different content")
	}
}
func TestCreateAccountIsolationAndChangedLogin(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	r := createRequest()
	alice := fakeCreator(t, filepath.Join(t.TempDir(), "alice"), r.AccountID, &calls, false)
	bob := fakeCreator(t, filepath.Join(t.TempDir(), "bob"), "ou_bob12345678", &calls, false)
	if _, err := bob.Run(ctx, r); err == nil || calls.Load() != 0 {
		t.Fatal("account mismatch executed", err)
	}
	for _, e := range []Executor{alice, bob} {
		if e.ReceiptDir == bob.ReceiptDir {
			r.AccountID = "ou_bob12345678"
		}
		text, err := e.Run(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		var receipt CreateReceipt
		json.Unmarshal([]byte(text), &receipt)
		if receipt.Account.OpenID != r.AccountID {
			t.Fatal("wrong identity", text)
		}
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}
func TestUnknownCreationIsNeverRetried(t *testing.T) {
	ctx := context.Background()
	r := createRequest()
	var calls atomic.Int32
	e := fakeCreator(t, filepath.Join(t.TempDir(), "receipts"), r.AccountID, &calls, true)
	for i := 0; i < 2; i++ {
		text, err := e.Run(ctx, r)
		if err != nil || !strings.Contains(text, `"state":"unknown"`) || strings.Contains(text, "secret") {
			t.Fatal(text, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("timeout replayed", calls.Load())
	}
}
func TestConcurrentCreateExecutesOnce(t *testing.T) {
	ctx := context.Background()
	r := createRequest()
	var calls atomic.Int32
	e := fakeCreator(t, filepath.Join(t.TempDir(), "receipts"), r.AccountID, &calls, false)
	// Initialize schema, then race separate database connections as two HTTP requests would.
	db, err := e.ledger()
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Run(ctx, r); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate external writes", calls.Load())
	}
}
func TestCreateRejectsInvalidPayload(t *testing.T) {
	for _, alter := range []func(*Request){
		func(r *Request) { r.Title = "" }, func(r *Request) { r.Content = strings.Repeat("中", 8001) },
		func(r *Request) { r.Content = "a\x01b" }, func(r *Request) { r.AccountID = "bot" },
		func(r *Request) { r.Target = "parent-token" }, func(r *Request) { r.RequestID = "" },
		func(r *Request) { r.Query = "work" }, func(r *Request) { r.Action = "feishu.document" },
	} {
		r := createRequest()
		alter(&r)
		if r.Validate() == nil {
			t.Fatal("accepted", r.Action)
		}
	}
}

func TestCreationRejectsBotAndExpiredIdentity(t *testing.T) {
	for _, body := range []string{
		`{"identity":"bot","available":true,"tokenStatus":"ready","onBehalfOf":{"openId":"ou_alice123456"}}`,
		`{"identity":"user","available":true,"tokenStatus":"expired","onBehalfOf":{"openId":"ou_alice123456"}}`,
		`{"identity":"user","available":false,"tokenStatus":"ready","onBehalfOf":{"openId":"ou_alice123456"}}`,
	} {
		e := Executor{ReceiptDir: filepath.Join(t.TempDir(), "receipts"), command: func(_ context.Context, args []string, _ string) ([]byte, error) {
			if args[0] != "whoami" {
				t.Fatal("created without user identity")
			}
			return []byte(body), nil
		}}
		if _, err := e.Run(context.Background(), createRequest()); err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
}
