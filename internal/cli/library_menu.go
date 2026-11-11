package cli

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

var errInvalidLibraryTable = errors.New("无效的表编号")

func (a *app) libraryMenu(p packages.Package, input *bufio.Reader) error {
	if len(p.Library.Tables) == 0 {
		return nil
	}
	for {
		table, err := a.selectLibraryTable(p, input)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, errInvalidLibraryTable) {
			fmt.Fprintln(a.errOut, err)
			continue
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "\n%s / %s\n1 查看记录  2 新增记录  3 修改记录  4 删除记录  0 退出\n", p.DataName(), table.Name)
		choice, err := a.libraryLine(input, "选择操作 [0] ")
		if errors.Is(err, io.EOF) || choice == "" || choice == "0" {
			return nil
		}
		if err != nil {
			return err
		}
		if err := a.runLibraryMenuAction(p, table, input, strings.TrimSpace(choice)); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			fmt.Fprintln(a.errOut, err)
		}
	}
}

func (a *app) selectLibraryTable(p packages.Package, input *bufio.Reader) (librarydb.Table, error) {
	tables := p.Library.Tables
	if len(tables) == 1 {
		return tables[0], nil
	}
	for i, table := range tables {
		fmt.Fprintf(a.out, "%d. %s\n", i+1, table.Name)
	}
	answer, err := a.libraryLine(input, "选择表（回车退出） ")
	if err != nil {
		return librarydb.Table{}, err
	}
	if answer == "" {
		return librarydb.Table{}, io.EOF
	}
	selected, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || selected < 1 || selected > len(tables) {
		return librarydb.Table{}, errInvalidLibraryTable
	}
	return tables[selected-1], nil
}

func (a *app) runLibraryMenuAction(p packages.Package, table librarydb.Table, input *bufio.Reader, choice string) error {
	if choice == "1" {
		return a.viewLibraryRecords(p, table, input)
	}
	actions := map[string]string{"2": "add", "3": "update", "4": "delete"}
	action := actions[choice]
	if action == "" {
		return errors.New("无效的操作编号")
	}
	edit := librarydb.RecordEdit{Table: table.Name, Action: action}
	if action != "add" {
		key, proceed, err := a.readLibraryKey(table, input, "")
		if err != nil {
			return err
		}
		if !proceed {
			return nil
		}
		edit.Key = key
	}
	state, err := a.libraryFiles(p)
	if err != nil {
		return err
	}
	state.fingerprint, err = libraryFingerprint(state)
	if err != nil {
		return err
	}
	if action != "add" {
		columns, rows, err := librarydb.ViewRecords(state.work, p.ID, *p.Library, table.Name, librarydb.RecordView{Key: edit.Key})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Fprintln(a.out, "未找到记录。")
			return nil
		}
		a.displayLibraryRecords(columns, rows)
	}
	switch action {
	case "delete":
		confirm, err := a.libraryLine(input, "确认删除此记录 [y/N] ")
		if err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(confirm), "y") && !strings.EqualFold(strings.TrimSpace(confirm), "yes") {
			return nil
		}
	default:
		snapshot, err := librarydb.ValidateWorking(state.work, p.ID, *p.Library)
		if err != nil {
			return err
		}
		edit.Values, err = a.readLibraryValues(table, snapshot.Tables[table.Name], input, action == "update")
		if err != nil {
			return err
		}
		if action == "update" && len(edit.Values) == 0 {
			return nil
		}
	}
	if err := a.mutateLibrary(p, edit, "manual", &state); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "已保存。")
	return nil
}

func (a *app) readLibraryKey(table librarydb.Table, input *bufio.Reader, firstAnswer string) ([]any, bool, error) {
	keys := make([]any, 0, len(table.Keys))
	for _, name := range table.Keys {
		answer := firstAnswer
		firstAnswer = ""
		if answer == "" {
			var err error
			answer, err = a.libraryLine(input, fmt.Sprintf("键 %s（回车取消） ", libraryFieldPrompt(table, name)))
			if err != nil || answer == "" {
				return nil, false, err
			}
		}
		value, err := parseLibraryValue(answer)
		if err != nil {
			return nil, false, err
		}
		keys = append(keys, value)
	}
	return keys, true, nil
}

func (a *app) readLibraryValues(table librarydb.Table, schema librarydb.TableSchema, input *bufio.Reader, updating bool) (map[string]any, error) {
	fmt.Fprintln(a.out, "直接输入文本。用 \"\" 表示空文本，null 表示空值，hex: 后跟十六进制表示二进制。int: 和 real: 可显式输入数字。回车跳过字段。")
	values := make(map[string]any)
	for _, column := range schema.Columns {
		if column.Hidden != 0 || updating && slices.Contains(table.Keys, column.Name) {
			continue
		}
		line, err := a.libraryLine(input, libraryFieldPrompt(table, column.Name)+" ")
		if err != nil {
			return nil, err
		}
		if line == "" {
			continue
		}
		value, err := parseLibraryValue(line)
		if err != nil {
			return nil, fmt.Errorf("字段 %s：%w", column.Name, err)
		}
		values[column.Name] = value
	}
	return values, nil
}

func (a *app) libraryLine(input *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(a.out, prompt)
	line, err := input.ReadString('\n')
	if errors.Is(err, io.EOF) && line != "" {
		err = nil
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), err
}

func libraryDisplayValue(value any) string {
	switch value := value.(type) {
	case nil:
		return "null"
	case []byte:
		return "hex:" + hex.EncodeToString(value)
	case string:
		return strconv.Quote(value)
	case int64:
		return "int:" + strconv.FormatInt(value, 10)
	case float64:
		return "real:" + strconv.FormatFloat(value, 'g', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func parseLibraryValue(input string) (any, error) {
	switch {
	case input == "null":
		return nil, nil
	case strings.HasPrefix(input, "hex:"):
		return hex.DecodeString(input[len("hex:"):])
	case strings.HasPrefix(input, "int:"):
		return strconv.ParseInt(input[len("int:"):], 10, 64)
	case strings.HasPrefix(input, "real:"):
		return strconv.ParseFloat(input[len("real:"):], 64)
	case strings.HasPrefix(input, "\""):
		return strconv.Unquote(input)
	default:
		return input, nil
	}
}

// mutateLibrary commits a record and its audit entry in the current work database.
func (a *app) mutateLibrary(p packages.Package, edit librarydb.RecordEdit, source string, expected *libraryFiles) error {
	if p.Library == nil || source != "manual" && source != "plugin" {
		return errors.New("业务库记录的写入来源无效")
	}
	unlock, err := a.lockLibrary(p.ID)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := a.store.Find(p.ID)
	if err != nil || current.Version != p.Version || current.Library == nil {
		return errors.New("业务库的包已变更，请重新打开管理菜单")
	}
	before, err := a.libraryFiles(current)
	if err != nil {
		return err
	}
	if expected != nil {
		if before.registered != expected.registered {
			return errors.New("确认期间业务库已更新，请重新预览")
		}
		if err := checkLibraryGeneration(*expected); err != nil {
			return err
		}
	}
	if before.registered == "" && before.generation != "" {
		before, err = a.importLegacyLibraryLocked(current)
		if err != nil {
			return err
		}
	}
	if before.generation == "" {
		return a.editFirstWorkingLibrary(current, before, edit, source)
	}
	if err := editWorkingLibrary(current, before, edit, source); err != nil {
		return err
	}
	if err := flushLibraryAudit(before); err != nil {
		fmt.Fprintf(a.errOut, "业务库修改已保存，审计记录待同步：%v\n", err)
	}
	return nil
}

func (a *app) editFirstWorkingLibrary(p packages.Package, before libraryFiles, edit librarydb.RecordEdit, source string) error {
	state, err := newCatalogLibraryState(p, before)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(state.directory)
		}
	}()
	work := filepath.Join(state.directory, "work.library.db")
	if err := copyLibraryFile(before.work, work); err != nil {
		return err
	}
	if err := copyLibraryFile(before.baseline, filepath.Join(state.directory, "baseline.library.db")); err != nil {
		return err
	}
	staged := libraryFiles{work: work, audit: filepath.Join(state.directory, "audit.jsonl")}
	if err := editWorkingLibrary(p, staged, edit, source); err != nil {
		return err
	}
	if err := flushLibraryAudit(staged); err != nil {
		return err
	}
	if err := a.store.SetLibraryGeneration(p.ID, p.Version, before.registered, state.activation.Generation); err != nil {
		return err
	}
	published = true
	return nil
}

func editWorkingLibrary(p packages.Package, state libraryFiles, edit librarydb.RecordEdit, source string) error {
	if err := flushLibraryAudit(state); err != nil {
		return err
	}
	offset, err := auditSize(state.audit)
	if err != nil {
		return err
	}
	return librarydb.EditRecord(state.work, p.ID, *p.Library, edit, offset, func(key []any, fields []string) ([]byte, error) {
		entry := libraryAuditEntry{Time: time.Now().UTC(), Source: source, Action: edit.Action, Version: p.Version}
		if len(fields) == 0 {
			fields = []string{""}
		}
		key = libraryAuditKey(edit.Table, key, *p.Library)
		for _, field := range fields {
			entry.Changes = append(entry.Changes, libraryAuditChange{
				Table: edit.Table, Key: key, Field: field, Action: edit.Action, Selected: "local",
			})
		}
		line, err := json.Marshal(entry)
		return append(line, '\n'), err
	})
}
