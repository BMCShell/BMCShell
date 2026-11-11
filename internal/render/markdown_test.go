package render

import (
	"strings"
	"testing"
)

func TestMarkdownBlocksAndInline(t *testing.T) {
	source := strings.Join([]string{
		"# Redfish 手册",
		"",
		"**会话**通过 [接口][ref] 创建。",
		"",
		"[ref]: /redfish/v1/SessionService",
		"",
		"> 行一  ",
		"> 行二",
		"",
		"3. 父项",
		"   - [x] 子项",
		"",
		"~~~json",
		"",
		"{",
		"\t\"literal\": \"&amp; **value**\"",
		"}",
		"~~~",
	}, "\r\n")

	want := strings.Join([]string{
		"Redfish 手册",
		"────────────",
		"",
		"会话通过 接口 (/redfish/v1/SessionService) 创建。",
		"",
		"│ 行一",
		"│ 行二",
		"",
		"3. 父项",
		"   • [x] 子项",
		"",
		"",
		"    {",
		"    \t\"literal\": \"&amp; **value**\"",
		"    }",
	}, "\n")

	if got := Markdown(source, 80); got != want {
		t.Fatalf("unexpected Markdown rendering:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarkdownWrapsTableWithoutLosingCells(t *testing.T) {
	source := "| 字段 | 描述 |\n| :--- | ---: |\n| **名称** | `a\\|b`<br>这是中文注释<br>abc defgh ijkl |\n"
	output := Markdown(source, 19)
	for _, value := range []string{"┌", "├", "└", "字段", "描述", "名称", "a|b", "这是中文", "注释", "defgh", "ijkl"} {
		if !strings.Contains(output, value) {
			t.Errorf("missing %q in table:\n%s", value, output)
		}
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := textWidth(line); width != 19 {
			t.Errorf("table line is %d cells, want 19: %q", width, line)
		}
	}
}

func TestMarkdownUnevenTableRows(t *testing.T) {
	source := "| 字段 | 描述 |\n| --- | --- |\n| 名称 | 可见值 | 超出列数 |\n| 缺少值 |\n"
	output := Markdown(source, 40)
	if !strings.Contains(output, "可见值") || !strings.Contains(output, "缺少值") || strings.Contains(output, "超出列数") {
		t.Fatalf("unexpected Markdown table with uneven rows:\n%s", output)
	}
}

func TestMarkdownEntityControlsStayLiteral(t *testing.T) {
	source := "&#27;[31m &#x9b; &Tab; &NewLine; &lrm; &amp; \\&amp; `&amp;`"
	want := "&#27;[31m &#x9b; &Tab; &NewLine; &lrm; & &amp; &amp;"
	if got := Markdown(source, 80); got != want {
		t.Fatalf("Markdown(%q) = %q, want %q", source, got, want)
	}
}
