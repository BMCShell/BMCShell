package librarydb

import (
	"database/sql"
	"fmt"
	"strings"
)

// Query looks up a value by the declared table key, in the contract's key order.
func Query(path, id string, contract Contract, keys []string) (string, error) {
	table := contract.lookupTable()
	if table.Name == "" || contract.Lookup.Value == "" {
		return "", fmt.Errorf("此业务库未声明查询规则")
	}
	if len(keys) != len(table.Keys) {
		return "", fmt.Errorf("业务库查询需要 %d 个键值：%s", len(table.Keys), strings.Join(table.Keys, ", "))
	}
	db, err := OpenReadOnly(path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	snapshot, err := inspect(tx, id)
	if err != nil {
		return "", err
	}
	if err := ValidateSnapshot(snapshot, contract); err != nil {
		return "", err
	}
	predicates := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, key := range table.Keys {
		predicates[i] = QuoteIdentifier(key) + " COLLATE BINARY = ?"
		args[i] = keys[i]
	}
	query := "SELECT " + QuoteIdentifier(contract.Lookup.Value) + " FROM " + QuoteIdentifier(table.Name) +
		" WHERE " + strings.Join(predicates, " AND ") + " LIMIT 2"
	values, err := queryRows(tx, func(rows *sql.Rows) (string, error) {
		var value string
		err := rows.Scan(&value)
		return value, err
	}, query, args...)
	if err != nil {
		return "", err
	}
	switch len(values) {
	case 0:
		return "", fmt.Errorf("未找到对应的业务库记录")
	case 1:
		return values[0], nil
	default:
		return "", fmt.Errorf("业务库键值重复")
	}
}
