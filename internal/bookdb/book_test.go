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
