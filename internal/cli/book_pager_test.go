package cli

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestBookPageLineOffsets(t *testing.T) {
	const header = "编号  标题\n"
	longLine := strings.Repeat("甲乙丙", bookIndexStride) + "丁"
	input := strings.NewReader(header + longLine + "\nshort\n")
	source := io.NewSectionReader(input, 0, input.Size())
	screen, err := indexBookScreen(source, 6, int64(len(header)))
	if err != nil || screen.headerRows != 2 || screen.rows != bookIndexStride+4 {
		t.Fatalf("rows = %d, header rows = %d, error = %v", screen.rows, screen.headerRows, err)
	}
	var out bytes.Buffer
	writer := bufio.NewWriter(&out)
	if err := screen.writeRows(writer, 0, screen.headerRows); err != nil {
		t.Fatal(err)
	}
	if err := screen.writeRows(writer, bookIndexStride+2, screen.rows); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "编号  \r\n标题\r\n丁\r\nshort\r\n"; got != want {
		t.Fatalf("page = %q, want %q", got, want)
	}
}

func TestBookOutputSpillsWithoutChangingContent(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", t.TempDir())
	var output bookOutput
	defer output.close()
	prefix := "header\n"
	body := strings.Repeat("book entry\n", 30000)
	for _, text := range []string{prefix, body} {
		if n, err := io.WriteString(&output, text); err != nil || n != len(text) {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if output.file == nil {
		t.Fatal("large query output did not spill")
	}
	reader, err := output.reader()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != prefix+body {
		t.Fatalf("spooled %d bytes, error = %v", len(got), err)
	}
}
