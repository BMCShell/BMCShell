package librarydb

import "errors"

type LegacyKeyValue struct {
	Key   *string `json:"key"`
	Value *string `json:"value"`
}

// ImportLegacyKeyValues replaces rows in a staged table using the declared key and value column.
func ImportLegacyKeyValues(path string, table Table, valueColumn string, entries []LegacyKeyValue) error {
	if len(table.Keys) != 1 {
		return errors.New("导入旧版业务库需要一个稳定键")
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
	target := QuoteIdentifier(table.Name)
	if _, err := tx.Exec("DELETE FROM " + target); err != nil {
		return err
	}
	insert, err := tx.Prepare("INSERT INTO " + target + " (" + quotedList([]string{table.Keys[0], valueColumn}) + ") VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer insert.Close()
	for _, entry := range entries {
		if entry.Key == nil || entry.Value == nil {
			return errors.New("旧版业务库的记录须同时包含键和值")
		}
		if _, err := insert.Exec(*entry.Key, *entry.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}
