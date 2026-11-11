package librarydb

import "errors"

type LegacyKeyValue struct {
	Key   *string `json:"key"`
	Value *string `json:"value"`
}

// ImportLegacyKeyValues replaces rows in a staged key/value snapshot.
func ImportLegacyKeyValues(path string, entries []LegacyKeyValue) error {
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
	if _, err := tx.Exec("DELETE FROM records"); err != nil {
		return err
	}
	insert, err := tx.Prepare("INSERT INTO records (key, value) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer insert.Close()
	for _, entry := range entries {
		if entry.Key == nil || entry.Value == nil {
			return errors.New("legacy Library records need a key and a value")
		}
		if _, err := insert.Exec(*entry.Key, *entry.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}
