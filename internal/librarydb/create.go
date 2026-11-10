package librarydb

import (
	"fmt"
	"os"
	"strings"
)

const createSchema = `
CREATE TABLE _bmcshell_library (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  id TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  data_version TEXT NOT NULL
);
CREATE TABLE records (key TEXT NOT NULL PRIMARY KEY, value TEXT NOT NULL);
PRAGMA user_version = 1;
`

// ExampleContract describes the database created by CreateExample.
func ExampleContract() Contract {
	return Contract{
		SchemaVersion: 1,
		Tables:        []Table{{Name: "records", Keys: []string{"key"}}},
		Lookup:        Lookup{Table: "records", Value: "value"},
	}
}

// CreateExample writes an unused .library.db with one fictional example record.
func CreateExample(path, id string) (err error) {
	if !strings.HasSuffix(strings.ToLower(path), ".library.db") || !libraryID.MatchString(id) {
		return fmt.Errorf("Library template needs a .library.db path and a valid package UUID")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err := f.Close(); err != nil {
		return err
	}
	db, err := OpenReadWrite(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); err == nil {
			err = closeErr
		}
	}()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(createSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO _bmcshell_library (singleton, id, schema_version, data_version) VALUES (1, ?, 1, '1')`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO records (key, value) VALUES (?, ?)`, "DEMO01", "示例设备 A"); err != nil {
		return err
	}
	return tx.Commit()
}
