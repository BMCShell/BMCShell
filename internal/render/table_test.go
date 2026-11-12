package render

import (
	"strings"
	"testing"
)

func TestTextTableWrapsAndSeparatesRows(t *testing.T) {
	var out strings.Builder
	const width = 17
	table := NewTextTable(&out, width, []string{"名称", "说明"}, []int{4, 10})
	if out.Len() != 0 {
		t.Fatal("empty table wrote output")
	}
	table.Row([]string{"包名示例", "原生查询接口"})
	if !strings.Contains(out.String(), "│ 包名 │ 原生查 │\n│ 示例 │ 询接口 │\n") {
		t.Fatalf("wide text did not wrap and align:\n%s", out.String())
	}
	table.Row([]string{"工具"})
	table.Close()
	table.Close()
	if strings.Count(out.String(), "├") != 2 || strings.Count(out.String(), "└") != 1 {
		t.Fatalf("wrong row separators or closing border:\n%s", out.String())
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if got := textWidth(line); got != width {
			t.Errorf("line is %d cells, want %d: %q", got, width, line)
		}
	}
}

func TestWrapTextAtLeastOneWideRune(t *testing.T) {
	got := strings.Join(WrapText("甲乙\n丙", 1), "/")
	if got != "甲/乙/丙" {
		t.Errorf("wrapped text = %q", got)
	}
}
