package resources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Append accepts a concrete docx URL only; wiki resolution and destructive edits
// are deliberately not part of this capability.
func ValidAppendTarget(target string) bool {
	u, err := url.Parse(target)
	return err == nil && validDocument(target) && u.Scheme == "https" && u.RawQuery == "" && u.Fragment == "" && strings.HasPrefix(u.Path, "/docx/") && !strings.HasSuffix(u.Path, "/")
}
func ContentDigest(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }
func paragraphXML(content string) string {
	return strings.TrimPrefix(documentXML("", content), "<title></title>")
}

// Extract text from the full XML document, excluding its title. Literal XML
// supplied by the user remains escaped by paragraphXML and is never interpreted.
func documentBody(source string) (string, error) {
	d := xml.NewDecoder(strings.NewReader("<root>" + source + "</root>"))
	var b strings.Builder
	titleDepth := 0
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch v := t.(type) {
		case xml.StartElement:
			if v.Name.Local == "title" || titleDepth > 0 {
				titleDepth++
			}
		case xml.EndElement:
			if titleDepth > 0 {
				titleDepth--
				continue
			}
			switch v.Name.Local {
			case "p", "h1", "h2", "h3", "li":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if titleDepth == 0 {
				b.Write(v)
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " "), nil
}
func (e Executor) readDocumentBody(ctx context.Context, target string) (string, error) {
	b, err := e.lark(ctx, []string{"docs", "+fetch", "--as", "user", "--doc", target, "--doc-format", "xml", "--json"}, "")
	if err != nil {
		return "", errors.New("无法读取完整正文")
	}
	var v struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Data     struct {
			Document struct {
				Content string `json:"content"`
			} `json:"document"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &v) != nil || !v.OK || v.Identity != "user" {
		return "", errors.New("无法读取完整正文")
	}
	body, err := documentBody(v.Data.Document.Content)
	return body, err
}

func (e Executor) documentHasContent(ctx context.Context, target, content string) bool {
	body, err := e.readDocumentBody(ctx, target)
	expected := strings.Join(strings.Fields(content), " ")
	return err == nil && expected != "" && strings.Contains(body, expected)
}

// RecentDocuments contains only typed local receipts, never raw CLI output.
func (e Executor) RecentDocuments(ctx context.Context) ([]CreateReceipt, error) {
	db, err := e.ledger()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT receipt FROM creates ORDER BY rowid DESC LIMIT 10")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CreateReceipt{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r CreateReceipt
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
