package bookdb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"bmcshell/internal/envregistry"
)

const bodyLimit = 2 << 20

var ErrNoMatch = errors.New("no matching book entry")

type operation struct {
	where  string
	values []any
	detail bool
}

// Query renders a list, literal search, or metadata-defined detail lookup.
func Query(out io.Writer, path string, args []string, cid string) error {
	registry, err := envregistry.LoadFramework("")
	if err != nil {
		return err
	}
	values := make(map[string]string)
	for name, field := range registry {
		if !field.Sensitive {
			if value, present := os.LookupEnv(name); present {
				values[name] = value
			}
		}
	}
	return QueryWithEnvironment(out, path, args, cid, registry, values)
}

// QueryWithEnvironment renders only the registered public values supplied by the caller.
func QueryWithEnvironment(out io.Writer, path string, args []string, cid string, registry envregistry.Registry, values map[string]string) error {
	db, meta, err := openBook(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		meta.renderHelp(out)
		return nil
	}
	rendering, err := meta.rendering(cid, registry, values)
	if err != nil {
		return err
	}
	op, err := meta.parse(args, rendering.enterprise)
	if err != nil {
		return err
	}
	columns := quoted(meta.List)
	if op.detail {
		body := quote(meta.Body)
		columns = append(columns, fmt.Sprintf("CASE WHEN length(CAST(%s AS BLOB)) <= %d THEN %s END", body, bodyLimit, body),
			"coalesce(length(CAST("+body+" AS BLOB)), 0)")
	}
	query := "SELECT " + strings.Join(columns, ", ") + " FROM " + quote(meta.Table) + op.where +
		" ORDER BY " + strings.Join(quoted(meta.Order), ", ")
	rows, err := db.Query(query, op.values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !op.detail {
		titles := make([]string, len(meta.List))
		for i, field := range meta.List {
			titles[i] = meta.title(field)
		}
		meta.writeListRow(out, titles)
	}
	matched := false
	for rows.Next() {
		matched = true
		values, destinations := scanTargets(len(columns))
		if err := rows.Scan(destinations...); err != nil {
			return err
		}
		shown := make([]string, len(meta.List))
		for i, field := range meta.List {
			shown[i], err = meta.display(field, values[i], rendering)
			if err != nil {
				return err
			}
		}
		if !op.detail {
			meta.writeListRow(out, shown)
			continue
		}
		length, _ := values[len(meta.List)+1].(int64)
		if length > bodyLimit {
			return errors.New("book entry body exceeds 2 MiB")
		}
		body, _ := values[len(meta.List)].(string)
		if body == "" {
			body = "暂无详细内容"
		}
		body, err = rendering.apply(meta.Body, meta.Options[meta.Body].Format, body)
		if err != nil {
			return err
		}
		for i, field := range meta.List {
			fmt.Fprintf(out, "%s: %s\n", meta.title(field), shown[i])
		}
		fmt.Fprintln(out, safe(body))
		fmt.Fprintln(out)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !matched {
		fmt.Fprintln(out, "未找到匹配条目。")
		return ErrNoMatch
	}
	return nil
}

func scanTargets(count int) ([]any, []any) {
	values := make([]any, count)
	destinations := make([]any, count)
	for i := range values {
		destinations[i] = &values[i]
	}
	return values, destinations
}

func quote(name string) string { return `"` + name + `"` }

func quoted(names []string) []string {
	result := make([]string, len(names))
	for i, name := range names {
		result[i] = quote(name)
	}
	return result
}

func (m metadata) parse(args []string, enterprise string) (operation, error) {
	if len(args) == 0 || len(args) == 1 && (args[0] == "-l" || args[0] == "--list") {
		return operation{}, nil
	}
	if args[0] == "-s" || args[0] == "--search" {
		if len(args) != 2 || len(m.Search) == 0 || !validArgument(args[1]) {
			return operation{}, errors.New("this book requires one valid search term")
		}
		parts, values := make([]string, len(m.Search)), make([]any, len(m.Search))
		for i, field := range m.Search {
			parts[i] = "instr(lower(" + quote(field) + "), lower(?)) > 0"
			values[i] = args[1]
		}
		return operation{where: " WHERE " + strings.Join(parts, " OR "), values: values}, nil
	}
	if !m.Details {
		return operation{}, errors.New("this book does not support detail lookup")
	}
	return m.parseLookup(args, enterprise)
}

func (m metadata) parseLookup(args []string, enterprise string) (operation, error) {
	var option string
	values := args
	if strings.HasPrefix(args[0], "-") {
		option, values = args[0], args[1:]
	} else if len(args) == 1 && strings.HasPrefix(args[0], "[") {
		if !strings.HasSuffix(args[0], "]") {
			return operation{}, errors.New("lookup brackets must be paired")
		}
		values = strings.Fields(args[0][1 : len(args[0])-1])
	}
	for _, rule := range m.Lookups {
		if len(rule.Fields) != len(values) || (option == "" && !rule.Positional) || (option != "" && rule.Option != option) {
			continue
		}
		parts, bound := make([]string, 0, len(values)), make([]any, 0, len(values))
		for i, field := range rule.Fields {
			value, err := m.lookupValue(field, values[i], enterprise)
			if err != nil {
				return operation{}, err
			}
			comparison := quote(field) + " = ? COLLATE NOCASE"
			if m.Options[field].Format == "oid" {
				canonical := swapOIDPrefix(value.(string), "enterprises", enterpriseOIDTemplate)
				parts = append(parts, "("+comparison+" OR "+comparison+" OR "+comparison+")")
				bound = append(bound, value, canonical, values[i])
			} else {
				parts = append(parts, comparison)
				bound = append(bound, value)
			}
		}
		return operation{where: " WHERE " + strings.Join(parts, " AND "), values: bound, detail: true}, nil
	}
	return operation{}, errors.New("unsupported book lookup or incorrect number of arguments; use bmc book <book> --help")
}

func validArgument(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func (m metadata) lookupValue(field, value, enterprise string) (any, error) {
	if !validArgument(value) {
		return nil, fmt.Errorf("invalid lookup argument for %s", field)
	}
	format := m.Options[field].Format
	switch format {
	case "integer":
		if value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				return n, nil
			}
		}
	case "hex8":
		value = strings.TrimPrefix(strings.ToLower(value), "0x")
		if len(value) >= 1 && len(value) <= 2 {
			if n, err := strconv.ParseUint(value, 16, 8); err == nil && (field != "netfn" || n <= 63) {
				return int64(n), nil
			}
		}
	case "oid":
		value = swapOIDPrefix(value, enterpriseOIDTemplate, "enterprises")
		value = swapOIDPrefix(value, canonicalEnterprisePrefix(enterprise), "enterprises")
		return swapOIDPrefix(value, enterprise, "enterprises"), nil
	default:
		return value, nil
	}
	return nil, fmt.Errorf("invalid %s lookup for %s", format, field)
}

func (m metadata) renderHelp(out io.Writer) {
	fmt.Fprintln(out, safe(m.Title))
	fmt.Fprintln(out, "-l / --list：列表")
	if len(m.Search) != 0 {
		fmt.Fprintln(out, "-s / --search 关键词：搜索")
	}
	for _, rule := range m.Lookups {
		fmt.Fprintln(out, rule.Option+" "+strings.Join(rule.Fields, " "))
	}
	if m.Help != "" {
		fmt.Fprintln(out, safe(m.Help))
	}
	if m.Source != "" {
		fmt.Fprintln(out, "来源："+safe(m.Source))
	}
}

func (m metadata) title(field string) string {
	if title := m.Options[field].Title; title != "" {
		return safe(title)
	}
	return field
}

func (m metadata) display(field string, value any, rendering bookRendering) (string, error) {
	n, integer := value.(int64)
	switch m.Options[field].Format {
	case "support":
		if !integer {
			return "未知", nil
		}
		if n == 1 {
			return "支持", nil
		}
		if n == 0 {
			return "不支持", nil
		}
		return "未知", nil
	case "hex8":
		if integer && n >= 0 && n <= 255 {
			return fmt.Sprintf("0x%02x", n), nil
		}
	}
	if value == nil {
		return "-", nil
	}
	text, err := rendering.apply(field, m.Options[field].Format, fmt.Sprint(value))
	return safe(text), err
}

func oidPrefix(cid string) string {
	switch cid {
	case "":
		return ""
	case "C05":
		return "1.3.6.1.4.1.54357"
	case "L0J":
		return "1.3.6.1.4.1.37945"
	default:
		return "1.3.6.1.4.1.0"
	}
}

func safe(value string) string {
	var result strings.Builder
	for _, r := range value {
		switch {
		case r == '\n':
			result.WriteRune(r)
		case r == '\t':
			result.WriteString("    ")
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			fmt.Fprintf(&result, "\\u%04x", r)
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}

func (m metadata) writeListRow(out io.Writer, fields []string) {
	columns := make([]string, len(fields))
	for i, field := range fields {
		width := m.Options[m.List[i]].Width
		if width == 0 {
			width = 24
		}
		columns[i] = fit(strings.ReplaceAll(field, "\n", " "), width)
	}
	fmt.Fprintln(out, strings.TrimRight(strings.Join(columns, "  "), " "))
}

func fit(text string, width int) string {
	used := 0
	for i, r := range text {
		cells := runeWidth(r)
		if used+cells > width || (used+cells == width && i+utf8.RuneLen(r) < len(text)) {
			return text[:i] + "…" + strings.Repeat(" ", width-used-1)
		}
		used += cells
	}
	return text + strings.Repeat(" ", width-used)
}

func runeWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if unicode.In(r, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana) ||
		(r >= 0x3000 && r <= 0x303f) || (r >= 0xff01 && r <= 0xff60) {
		return 2
	}
	return 1
}
