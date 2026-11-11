package librarydb

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// RecordEdit addresses one declared business table and stable key.
type RecordEdit struct {
	Action string
	Table  string
	Key    []any
	Values map[string]any
}

func declaredTable(contract Contract, name string) (Table, error) {
	for _, table := range contract.Tables {
		if table.Name == name {
			return table, nil
		}
	}
	return Table{}, fmt.Errorf("业务库未登记表 %q", name)
}

func recordFilter(table Table, key []any) (string, error) {
	if len(key) != len(table.Keys) || slices.Contains(key, nil) {
		return "", fmt.Errorf("表 %q 需要 %d 个非空键值", table.Name, len(table.Keys))
	}
	var predicates []string
	for _, field := range table.Keys {
		predicates = append(predicates, QuoteIdentifier(field)+" COLLATE BINARY = ?")
	}
	return " WHERE " + strings.Join(predicates, " AND "), nil
}

// ReadRecords reads one record by key, or the first limit records in stable key order.
func ReadRecords(path, id string, contract Contract, tableName string, key []any, limit int) ([]string, [][]any, error) {
	return readRecords(path, id, contract, tableName, RecordView{Key: key, Limit: limit}, false)
}

// RecordView selects a record by key, or a window in stable key order.
// Search is a literal substring and an empty Field searches every view column.
type RecordView struct {
	Key    []any
	Field  string
	Search string
	Offset int
	Limit  int
}

// ViewColumns reports the ordered columns that can be displayed and searched.
func ViewColumns(path, id string, contract Contract, tableName string) ([]string, error) {
	table, err := declaredTable(contract, tableName)
	if err != nil {
		return nil, err
	}
	snapshot, err := ValidateWorking(path, id, contract)
	if err != nil {
		return nil, err
	}
	return recordColumns(snapshot.Tables[table.Name], table, contract, true), nil
}

// ViewRecords returns only viewable columns, using the same rules for search.
func ViewRecords(path, id string, contract Contract, tableName string, view RecordView) ([]string, [][]any, error) {
	if view.Offset < 0 || view.Key == nil && view.Limit <= 0 {
		return nil, nil, errors.New("无效的记录范围")
	}
	if view.Key != nil && (view.Search != "" || view.Field != "" || view.Offset != 0) {
		return nil, nil, errors.New("按键查看时不能同时指定搜索或起始位置")
	}
	if view.Field != "" && view.Search == "" {
		return nil, nil, errors.New("请输入搜索内容")
	}
	return readRecords(path, id, contract, tableName, view, true)
}

func recordColumns(schema TableSchema, table Table, contract Contract, viewOnly bool) []string {
	if viewOnly && table.ViewColumns != nil {
		return slices.Clone(table.ViewColumns)
	}
	var columns []string
	for _, column := range schema.Columns {
		if column.Hidden != 1 && (!viewOnly || !contract.Sensitive(table.Name, column.Name)) {
			columns = append(columns, column.Name)
		}
	}
	return columns
}

func checkedRecordSnapshot(tx *sql.Tx, id string, contract Contract) (Snapshot, error) {
	if err := quickCheck(tx); err != nil {
		return Snapshot{}, err
	}
	snapshot, err := inspect(tx, id)
	if err == nil {
		err = ValidateSnapshot(snapshot, contract)
	}
	return snapshot, err
}

func readRecords(path, id string, contract Contract, tableName string, view RecordView, viewOnly bool) ([]string, [][]any, error) {
	table, err := declaredTable(contract, tableName)
	if err != nil {
		return nil, nil, err
	}
	db, err := OpenReadOnly(path)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	snapshot, err := checkedRecordSnapshot(tx, id, contract)
	if err != nil {
		return nil, nil, err
	}
	columns := recordColumns(snapshot.Tables[table.Name], table, contract, viewOnly)
	clause, args, err := recordViewClause(table, columns, view)
	if err != nil {
		return nil, nil, err
	}
	columnCount := len(columns)
	selection := quotedList(columns)
	if columnCount == 0 {
		if view.Key == nil {
			return columns, nil, nil
		}
		selection, columnCount = "1", 1 // Exact-key previews may only reveal that the record exists.
	}
	query := "SELECT " + selection + " FROM " + QuoteIdentifier(table.Name) + clause
	records, err := queryRows(tx, func(rows *sql.Rows) ([]any, error) {
		record, destinations := recordBuffers(columnCount)
		err := rows.Scan(destinations...)
		return record[:len(columns)], err
	}, query, args...)
	return columns, records, err
}

func recordViewClause(table Table, columns []string, view RecordView) (string, []any, error) {
	if view.Key != nil {
		filter, err := recordFilter(table, view.Key)
		return filter, view.Key, err
	}
	var clause string
	var args []any
	if view.Search != "" {
		if view.Field != "" {
			if !slices.Contains(columns, view.Field) {
				return "", nil, fmt.Errorf("字段 %q 不可搜索", view.Field)
			}
			columns = []string{view.Field}
		}
		predicates := make([]string, len(columns))
		for i, column := range columns {
			name := QuoteIdentifier(column)
			text := "CASE WHEN typeof(" + name + ") = 'blob' THEN 'hex:' || lower(hex(" + name + ")) ELSE CAST(" + name + " AS TEXT) END"
			predicates[i] = "instr(" + text + ", ?) > 0"
			args = append(args, view.Search)
		}
		clause = " WHERE " + strings.Join(predicates, " OR ")
	}
	order := make([]string, len(table.Keys))
	for i, name := range table.Keys {
		order[i] = QuoteIdentifier(name) + " COLLATE BINARY"
	}
	clause += " ORDER BY " + strings.Join(order, ", ") + " LIMIT ? OFFSET ?"
	return clause, append(args, view.Limit, view.Offset), nil
}

// EditRecord commits the record and its audit entry in the same SQLite transaction.
func EditRecord(path, id string, contract Contract, edit RecordEdit, offset int64, audit func([]any, []string) ([]byte, error)) error {
	table, err := declaredTable(contract, edit.Table)
	if err != nil {
		return err
	}
	db, err := OpenReadWrite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := checkedRecordSnapshot(tx, id, contract)
	if err != nil {
		return err
	}
	var columns []string
	var args []any
	for _, column := range snapshot.Tables[table.Name].Columns {
		value, exists := edit.Values[column.Name]
		if !exists || column.Hidden != 0 || edit.Action == "update" && slices.Contains(table.Keys, column.Name) {
			continue
		}
		columns = append(columns, column.Name)
		args = append(args, value)
	}
	if len(columns) != len(edit.Values) {
		return errors.New("包含不可编辑的字段")
	}
	target := QuoteIdentifier(table.Name)
	var query string
	switch edit.Action {
	case "add":
		query = "INSERT INTO " + target + " DEFAULT VALUES"
		if len(columns) != 0 {
			parameters := strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")
			query = "INSERT INTO " + target + " (" + quotedList(columns) + ") VALUES (" + parameters + ")"
		}
	case "update":
		if len(columns) == 0 {
			return errors.New("没有要保存的修改")
		}
		assignments := make([]string, len(columns))
		for i, name := range columns {
			assignments[i] = QuoteIdentifier(name) + " = ?"
		}
		query = "UPDATE " + target + " SET " + strings.Join(assignments, ", ")
	case "delete":
		if len(edit.Values) != 0 {
			return errors.New("删除记录只需要稳定键")
		}
		query = "DELETE FROM " + target
	default:
		return errors.New("无效的记录操作")
	}
	if edit.Action != "add" {
		filter, err := recordFilter(table, edit.Key)
		if err != nil {
			return err
		}
		query += filter
		args = append(args, edit.Key...)
	}
	query += " RETURNING " + storedColumnList(table.Keys)
	key, destinations := recordBuffers(len(table.Keys))
	if err := tx.QueryRow(query, args...).Scan(destinations...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.New("未找到要修改的记录")
		}
		return err
	}
	restoreStoredText(key)
	if err := validateEditedFields(tx, table, snapshot.Tables[table.Name], edit, key); err != nil {
		return err
	}
	line, err := audit(key, columns)
	if err != nil {
		return err
	}
	if err := queueAudit(tx, offset, line); err != nil {
		return err
	}
	return tx.Commit()
}

func validateEditedFields(tx *sql.Tx, table Table, schema TableSchema, edit RecordEdit, key []any) error {
	if len(table.Fields) == 0 || edit.Action == "delete" {
		return nil
	}
	var fields []string
	for _, column := range schema.Columns {
		rule := table.Fields[column.Name]
		_, supplied := edit.Values[column.Name]
		if (rule.Type != "" || rule.Required) && (edit.Action == "add" || supplied) {
			fields = append(fields, column.Name)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	filter, err := recordFilter(table, key)
	if err != nil {
		return err
	}
	values, destinations := recordBuffers(len(fields))
	query := "SELECT " + storedColumnList(fields) + " FROM " + QuoteIdentifier(table.Name) + filter
	if err := tx.QueryRow(query, key...).Scan(destinations...); err != nil {
		return fmt.Errorf("无法验证保存后的记录：%w", err)
	}
	restoreStoredText(values)
	for i, name := range fields {
		if err := table.Fields[name].validateValue(values[i]); err != nil {
			return fmt.Errorf("字段 %s：%w", name, err)
		}
	}
	return nil
}

func storedColumnList(columns []string) string {
	expressions := make([]string, len(columns))
	for i, name := range columns {
		// Unary plus keeps the SQLite value but prevents driver conversion based on the declared type.
		expressions[i] = "+" + QuoteIdentifier(name)
	}
	return strings.Join(expressions, ", ")
}

func restoreStoredText(values []any) {
	for i, value := range values {
		// For untyped expressions the driver only parses timestamps whose text round-trips exactly.
		if date, ok := value.(time.Time); ok {
			values[i] = date.Format(time.RFC3339Nano)
		}
	}
}

func recordBuffers(size int) ([]any, []any) {
	record := make([]any, size)
	destinations := make([]any, size)
	for i := range record {
		destinations[i] = &record[i]
	}
	return record, destinations
}
