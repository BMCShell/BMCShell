package cli

import (
	"bufio"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

const defaultLibraryViewLimit = 25

func (a *app) viewLibraryRecords(p packages.Package, table librarydb.Table, input *bufio.Reader) error {
	state, err := a.libraryFiles(p)
	if err != nil {
		return err
	}
	columns, err := librarydb.ViewColumns(state.work, p.ID, *p.Library, table.Name)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		fmt.Fprintln(a.out, "当前表没有可查看的表头。")
		return nil
	}
	fmt.Fprintln(a.out, "可查看表头")
	for _, name := range columns {
		fmt.Fprintf(a.out, "  %s\n", libraryFieldPrompt(table, name))
	}
	fmt.Fprintln(a.out, ":search 搜索内容  :range 按范围查看")
	answer, err := a.libraryLine(input, fmt.Sprintf("输入查看方式或键 %s（回车列出前 %d 条） ", table.Keys[0], defaultLibraryViewLimit))
	if err != nil {
		return err
	}

	var view librarydb.RecordView
	var bounds string
	switch answer {
	case ":search":
		field, err := a.libraryLine(input, "输入搜索列的表头（回车搜索全部可查看列） ")
		if err != nil {
			return err
		}
		if field != "" && !slices.Contains(columns, field) {
			return fmt.Errorf("不可搜索表头 %q", field)
		}
		search, err := a.libraryLine(input, "搜索内容（区分大小写，回车取消） ")
		if err != nil || search == "" {
			return err
		}
		view.Field, view.Search = field, search
		fallthrough
	case ":range":
		bounds, err = a.libraryLine(input, fmt.Sprintf("按键排序后的记录范围（起始-结束，从 1 开始，回车列出前 %d 条） ", defaultLibraryViewLimit))
		if err != nil {
			return err
		}
	case "":
	default:
		key, proceed, err := a.readLibraryKey(table, input, answer)
		if err != nil || !proceed {
			return err
		}
		view.Key = key
	}
	bounds = strings.TrimSpace(bounds)
	limit := defaultLibraryViewLimit
	switch {
	case bounds != "":
		view.Offset, view.Limit, err = parseLibraryRange(bounds)
		if err != nil {
			return err
		}
		limit = view.Limit
	case view.Key == nil:
		view.Limit = limit + 1
	}
	columns, rows, err := librarydb.ViewRecords(state.work, p.ID, *p.Library, table.Name, view)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.out, "未找到记录。")
		return nil
	}
	a.displayLibraryRecords(columns, rows[:min(limit, len(rows))])
	if len(rows) > limit {
		fmt.Fprintf(a.out, "已显示前 %d 条。可指定范围查看其他记录。\n", limit)
	} else if bounds != "" {
		fmt.Fprintf(a.out, "已显示第 %d–%d 条。\n", view.Offset+1, view.Offset+len(rows))
	}
	return nil
}

func parseLibraryRange(bounds string) (offset, limit int, err error) {
	left, right, found := strings.Cut(bounds, "-")
	start, startErr := strconv.Atoi(strings.TrimSpace(left))
	end, endErr := strconv.Atoi(strings.TrimSpace(right))
	if !found || startErr != nil || endErr != nil || start < 1 || end < start {
		return 0, 0, errors.New("无效的记录范围，格式为起始-结束，起始必须大于或等于 1")
	}
	return start - 1, end - start + 1, nil
}

func libraryFieldPrompt(table librarydb.Table, name string) string {
	if description := table.Fields[name].Description; description != "" {
		return name + "（" + description + "）"
	}
	return name
}

func (a *app) displayLibraryRecords(columns []string, rows [][]any) {
	if len(columns) == 0 {
		fmt.Fprintln(a.out, "找到记录。")
		return
	}
	fmt.Fprintln(a.out, strings.Join(columns, "\t"))
	values := make([]string, len(columns))
	for _, row := range rows {
		for i, value := range row {
			values[i] = libraryDisplayValue(value)
		}
		fmt.Fprintln(a.out, strings.Join(values, "\t"))
	}
}
