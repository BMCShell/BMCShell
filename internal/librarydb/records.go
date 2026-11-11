package librarydb

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	table, err := declaredTable(contract, tableName)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := ValidateWorking(path, id, contract)
	if err != nil {
		return nil, nil, err
	}
	var columns []string
	for _, column := range snapshot.Tables[table.Name].Columns {
		if column.Hidden != 1 {
			columns = append(columns, column.Name)
		}
	}
	query := "SELECT " + quotedList(columns) + " FROM " + QuoteIdentifier(table.Name)
	args := key
	if key != nil {
		filter, err := recordFilter(table, key)
		if err != nil {
			return nil, nil, err
		}
		query += filter
	} else {
		order := make([]string, len(table.Keys))
		for i, name := range table.Keys {
			order[i] = QuoteIdentifier(name) + " COLLATE BINARY"
		}
		query += " ORDER BY " + strings.Join(order, ", ") + " LIMIT ?"
		args = []any{limit}
	}
	db, err := OpenReadOnly(path)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var records [][]any
	for rows.Next() {
		record, destinations := recordBuffers(len(columns))
		if err := rows.Scan(destinations...); err != nil {
			return nil, nil, err
		}
		records = append(records, record)
	}
	return columns, records, rows.Err()
}

// EditRecord commits the record and its audit entry in the same SQLite transaction.
func EditRecord(path, id string, contract Contract, edit RecordEdit, offset int64, audit func([]any, []string) ([]byte, error)) error {
	table, err := declaredTable(contract, edit.Table)
	if err != nil {
		return err
	}
	snapshot, err := ValidateWorking(path, id, contract)
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
	query += " RETURNING " + quotedList(table.Keys)
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
	key, destinations := recordBuffers(len(table.Keys))
	if err := tx.QueryRow(query, args...).Scan(destinations...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.New("未找到要修改的记录")
		}
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

func recordBuffers(size int) ([]any, []any) {
	record := make([]any, size)
	destinations := make([]any, size)
	for i := range record {
		destinations[i] = &record[i]
	}
	return record, destinations
}
