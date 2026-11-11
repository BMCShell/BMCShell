package render

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var markdownParser = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

// Markdown renders Markdown as plain text. The width sets the target size
// for tables. Callers sanitize the rendered text before terminal output.
func Markdown(source string, width int) string {
	if width <= 0 {
		width = 120
	}
	body := markdownBody([]byte(strings.ReplaceAll(source, "\r\n", "\n")))
	document := markdownParser.Parse(text.NewReader(body))
	return body.blocks(document, width, "\n\n")
}

type markdownBody []byte

func (body markdownBody) blocks(parent ast.Node, width int, separator string) string {
	var out strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		block := body.block(child, width)
		if block == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString(separator)
		}
		out.WriteString(block)
	}
	return out.String()
}

func (body markdownBody) block(node ast.Node, width int) string {
	switch node := node.(type) {
	case *ast.Heading:
		heading := body.inline(node)
		if node.Level <= 2 && heading != "" {
			return heading + "\n" + strings.Repeat("─", max(1, min(width, textWidth(heading))))
		}
		return heading
	case *ast.ThematicBreak:
		return strings.Repeat("─", max(1, min(width, 24)))
	case *ast.CodeBlock, *ast.FencedCodeBlock:
		return prefixLines(strings.TrimSuffix(body.lines(node.Lines()), "\n"), "    ", "    ")
	case *ast.Blockquote:
		return prefixLines(body.blocks(node, width-2, "\n\n"), "│ ", "│ ")
	case *ast.List:
		return body.list(node, width)
	case *extast.Table:
		return body.table(node, width)
	case *ast.HTMLBlock:
		var out strings.Builder
		out.WriteString(body.lines(node.Lines()))
		if node.HasClosure() {
			closure := node.ClosureLine
			out.Write(closure.Value(body))
		}
		return strings.TrimSuffix(out.String(), "\n")
	default:
		return body.inline(node)
	}
}

func (body markdownBody) lines(lines *text.Segments) string {
	var out strings.Builder
	for i := range lines.Len() {
		line := lines.At(i)
		out.Write(line.Value(body))
	}
	return out.String()
}

func (body markdownBody) list(list *ast.List, width int) string {
	var items []string
	separator := "\n"
	if !list.IsTight {
		separator = "\n\n"
	}
	number := list.Start
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		marker := "• "
		if list.IsOrdered() {
			marker = strconv.Itoa(number) + ". "
			number++
		}
		indent := textWidth(marker)
		contents := body.blocks(item, width-indent, separator)
		items = append(items, prefixLines(contents, marker, strings.Repeat(" ", indent)))
	}
	return strings.Join(items, separator)
}

func prefixLines(value, first, rest string) string {
	var out strings.Builder
	for i, line := range strings.Split(value, "\n") {
		prefix := first
		if i > 0 {
			out.WriteByte('\n')
			prefix = rest
		}
		if line == "" {
			out.WriteString(strings.TrimRight(prefix, " "))
		} else {
			out.WriteString(prefix)
		}
		out.WriteString(line)
	}
	return out.String()
}

func (body markdownBody) inline(parent ast.Node) string {
	var out strings.Builder
	body.writeInline(&out, parent)
	return out.String()
}

func (body markdownBody) writeInline(out *strings.Builder, parent ast.Node) {
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		switch node := child.(type) {
		case *ast.Text:
			value := node.Value(body)
			if node.IsRaw() {
				out.Write(value)
			} else {
				out.WriteString(decodeInline(value))
			}
			if node.HardLineBreak() {
				out.WriteByte('\n')
			} else if node.SoftLineBreak() {
				out.WriteByte(' ')
			}
		case *ast.String:
			if node.IsRaw() || node.IsCode() {
				out.Write(node.Value)
			} else {
				out.WriteString(decodeInline(node.Value))
			}
		case *ast.CodeSpan:
			for span := node.FirstChild(); span != nil; span = span.NextSibling() {
				value := span.(*ast.Text).Value(body)
				out.WriteString(strings.ReplaceAll(string(value), "\n", " "))
			}
		case *ast.Link:
			writeLink(out, body.inline(node), decodeInline(node.Destination))
		case *ast.Image:
			writeLink(out, body.inline(node), decodeInline(node.Destination))
		case *ast.AutoLink:
			writeLink(out, string(node.Label(body)), string(node.URL(body)))
		case *ast.RawHTML:
			tag := string(node.Segments.Value(body))
			switch strings.ToLower(strings.TrimSpace(tag)) {
			case "<br>", "<br/>", "<br />":
				out.WriteByte('\n')
			default:
				out.WriteString(tag)
			}
		case *extast.TaskCheckBox:
			if node.IsChecked {
				out.WriteString("[x] ")
			} else {
				out.WriteString("[ ] ")
			}
		default:
			// Emphasis and strikethrough retain their text in the plain terminal view.
			body.writeInline(out, node)
		}
	}
}

func writeLink(out *strings.Builder, label, destination string) {
	if label == "" {
		out.WriteString(destination)
		return
	}
	out.WriteString(label)
	if destination != "" && destination != label {
		fmt.Fprintf(out, " (%s)", destination)
	}
}

var markdownEscapes = regexp.MustCompile(`\\[[:punct:]]|&(?:#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]*);`)

func decodeInline(value []byte) string {
	return markdownEscapes.ReplaceAllStringFunc(string(value), func(escaped string) string {
		if escaped[0] == '\\' {
			return escaped[1:]
		}
		var decoded string
		if escaped[1] == '#' {
			number, base := escaped[2:len(escaped)-1], 10
			if number[0] == 'x' || number[0] == 'X' {
				number, base = number[1:], 16
			}
			value, _ := strconv.ParseUint(number, base, 32)
			decoded = string(rune(value))
		} else if entity, ok := util.LookUpHTML5EntityByName(escaped[1 : len(escaped)-1]); ok {
			decoded = string(entity.Characters)
		}
		if decoded == "" || strings.ContainsFunc(decoded, func(r rune) bool {
			return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
		}) {
			return escaped
		}
		return decoded
	})
}
