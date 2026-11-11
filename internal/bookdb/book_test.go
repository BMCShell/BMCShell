package bookdb

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateAndQueryBook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "中文 % 示例 #1.book.db")
	err := Create(path, []Entry{{ID: "1", Title: "IPMI 示例", Body: "查询正文"}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Query(&out, path, []string{"-s", "ipmi"}, ""); err != nil || !strings.Contains(out.String(), "IPMI 示例") {
		t.Fatalf("search: %v\n%s", err, &out)
	}
	out.Reset()
	if err := Query(&out, path, []string{"--id", "1"}, ""); err != nil || !strings.Contains(out.String(), "查询正文") {
		t.Fatalf("detail: %v\n%s", err, &out)
	}
	if err := Query(&out, path, []string{"-s", "%"}, ""); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("expected literal search to miss: %v", err)
	}
	if err := Create(path, nil); err == nil {
		t.Fatal("Create overwrote an existing Book")
	}
}

func TestDetailPresentationUsesContentFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.book.db")
	body := "## 说明\n\n```json\n{\n  \"Enabled\": true\n}\n```"
	if err := Create(path, []Entry{{ID: "1", Title: "Example", Body: body}}); err != nil {
		t.Fatal(err)
	}
	args := []string{"--id", "1"}
	var out bytes.Buffer
	if err := QueryWithEnvironment(&out, path, args, "", nil, nil, Display{Width: 60}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), body) {
		t.Fatalf("plain detail changed:\n%s", &out)
	}
	db, err := open(path, "rw")
	if err != nil {
		t.Fatal(err)
	}
	_, updateErr := db.Exec("UPDATE book_meta SET content_format = 'markdown'")
	if err := errors.Join(updateErr, db.Close()); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Query(&out, path, args, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), body) {
		t.Fatalf("redirected Markdown changed:\n%s", &out)
	}
	out.Reset()
	if err := QueryWithEnvironment(&out, path, args, "", nil, nil, Display{Width: 60}); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if strings.Contains(rendered, "## 说明") || strings.Contains(rendered, "```") ||
		!strings.Contains(rendered, "说明") || !strings.Contains(rendered, "  \"Enabled\": true") {
		t.Fatalf("unexpected rendered detail:\n%s", rendered)
	}
}
