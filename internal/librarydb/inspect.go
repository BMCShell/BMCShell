package librarydb

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

type Snapshot struct {
	ID            string
	SchemaVersion int
	DataVersion   string
	Tables        map[string]TableSchema
}

type TableSchema struct {
	Name     string
	SQL      string
	Columns  []Column
	Indexes  []Index
	Triggers []Definition
}

type Column struct {
	Name       string
	Type       string
	NotNull    bool
	Default    *string
	PrimaryKey int
	Hidden     int
}

type Index struct {
	Definition
	Origin  string
	Unique  bool
	Partial bool
	Columns []IndexColumn
}

type IndexColumn struct {
	Name       *string
	Collation  string
	Descending bool
}

type Definition struct {
	Name string
	SQL  string
}

// Inspect reads the identity and table structure after checking SQLite integrity.
// It does not require a particular schema version so callers can report version changes.
func Inspect(path, id string) (Snapshot, error) {
	return inspectFile(path, id, false)
}

func inspectFile(path, id string, immutable bool) (Snapshot, error) {
	db, err := open(path, "ro", immutable)
	if err != nil {
		return Snapshot{}, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	if err := quickCheck(tx); err != nil {
		return Snapshot{}, err
	}
	return inspect(tx, id)
}

func quickCheck(tx *sql.Tx) error {
	var check string
	if err := tx.QueryRow("PRAGMA quick_check(1)").Scan(&check); err != nil {
		return fmt.Errorf("SQLite quick check: %w", err)
	}
	if check != "ok" {
		return errors.New("Library database failed the SQLite quick check")
	}
	return nil
}

func inspect(tx *sql.Tx, id string) (Snapshot, error) {
	snapshot, err := readIdentity(tx, id)
	if err != nil {
		return Snapshot{}, err
	}
	tables, err := queryRows(tx, func(rows *sql.Rows) (TableSchema, error) {
		var table TableSchema
		err := rows.Scan(&table.Name, &table.SQL)
		return table, err
	}, `SELECT t.name, s.sql FROM pragma_table_list AS t JOIN main.sqlite_schema AS s ON s.name = t.name
		WHERE t.schema = 'main' AND t.type = 'table' AND s.type = 'table' AND t.name <> ?
		AND substr(t.name, 1, 7) <> 'sqlite_' ORDER BY t.name`, metadataTable)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Tables = make(map[string]TableSchema, len(tables))
	for _, table := range tables {
		if err := inspectTable(tx, &table); err != nil {
			return Snapshot{}, fmt.Errorf("Library table %q: %w", table.Name, err)
		}
		snapshot.Tables[table.Name] = table
	}
	return snapshot, nil
}

func readIdentity(tx *sql.Tx, id string) (Snapshot, error) {
	var kind string
	if err := tx.QueryRow(`SELECT type FROM pragma_table_list WHERE schema = 'main' AND name = ?`, metadataTable).Scan(&kind); err != nil || kind != "table" {
		return Snapshot{}, fmt.Errorf("Library metadata %q must be an ordinary table", metadataTable)
	}
	entries, err := queryRows(tx, func(rows *sql.Rows) (Snapshot, error) {
		var meta Snapshot
		err := rows.Scan(&meta.ID, &meta.SchemaVersion, &meta.DataVersion)
		return meta, err
	}, `SELECT id, schema_version, data_version FROM main._bmcshell_library LIMIT 2`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("Library metadata: %w", err)
	}
	if len(entries) != 1 || !libraryID.MatchString(entries[0].ID) || entries[0].ID != id ||
		!validIdentifier(entries[0].DataVersion) || len(entries[0].DataVersion) > 128 {
		return Snapshot{}, errors.New("Library metadata must contain one row with matching package ID and a valid dataVersion")
	}
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return Snapshot{}, err
	}
	if version < 1 || version != entries[0].SchemaVersion {
		return Snapshot{}, errors.New("Library schemaVersion must match SQLite user_version")
	}
	return entries[0], nil
}

func inspectTable(tx *sql.Tx, table *TableSchema) error {
	columns, err := queryRows(tx, func(rows *sql.Rows) (Column, error) {
		var column Column
		err := rows.Scan(&column.Name, &column.Type, &column.NotNull, &column.Default, &column.PrimaryKey, &column.Hidden)
		return column, err
	}, `SELECT name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?) ORDER BY cid`, table.Name)
	if err != nil {
		return err
	}
	indexes, err := queryRows(tx, func(rows *sql.Rows) (Index, error) {
		var index Index
		err := rows.Scan(&index.Name, &index.SQL, &index.Origin, &index.Unique, &index.Partial)
		return index, err
	}, `SELECT i.name, coalesce(s.sql, ''), i.origin, i."unique", i.partial FROM pragma_index_list(?) AS i
		LEFT JOIN main.sqlite_schema AS s ON s.name = i.name AND s.type = 'index' ORDER BY i.name`, table.Name)
	if err != nil {
		return err
	}
	for i := range indexes {
		indexes[i].Columns, err = queryRows(tx, func(rows *sql.Rows) (IndexColumn, error) {
			var column IndexColumn
			err := rows.Scan(&column.Name, &column.Collation, &column.Descending)
			return column, err
		}, `SELECT name, coalesce(coll, ''), "desc" FROM pragma_index_xinfo(?) WHERE "key" = 1 ORDER BY seqno`, indexes[i].Name)
		if err != nil {
			return err
		}
	}
	triggers, err := queryRows(tx, func(rows *sql.Rows) (Definition, error) {
		var definition Definition
		err := rows.Scan(&definition.Name, &definition.SQL)
		return definition, err
	}, `SELECT name, sql FROM main.sqlite_schema WHERE type = 'trigger' AND tbl_name = ? ORDER BY name`, table.Name)
	if err != nil {
		return err
	}
	table.Columns, table.Indexes, table.Triggers = columns, indexes, triggers
	return nil
}

// Validate checks that a release template has the declared identity, version, tables and keys.
func Validate(path, id string, contract Contract) (Snapshot, error) {
	if !strings.HasSuffix(strings.ToLower(path), ".library.db") {
		return Snapshot{}, errors.New("Library data file must end in .library.db")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return Snapshot{}, fmt.Errorf("Library release database must be standalone (found %s sidecar)", suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, err
		}
	}
	snapshot, err := inspectFile(path, id, true)
	if err == nil {
		err = ValidateSnapshot(snapshot, contract)
	}
	return snapshot, err
}

func ValidateSnapshot(snapshot Snapshot, contract Contract) error {
	if err := contract.Validate(); err != nil {
		return err
	}
	if snapshot.SchemaVersion != contract.SchemaVersion {
		return fmt.Errorf("Library schema version %d does not match required version %d", snapshot.SchemaVersion, contract.SchemaVersion)
	}
	for _, declared := range contract.Tables {
		table, exists := snapshot.Tables[declared.Name]
		if !exists {
			return fmt.Errorf("Library table %q is missing or is not an ordinary table", declared.Name)
		}
		if !stableKey(table, declared.Keys) {
			return fmt.Errorf("Library table %q requires a non-null primary key or binary unique key on %s", declared.Name, strings.Join(declared.Keys, ", "))
		}
	}
	if contract.Lookup != (Lookup{}) {
		if _, ok := columnNamed(snapshot.Tables[contract.Lookup.Table], contract.Lookup.Value); !ok {
			return fmt.Errorf("Library lookup column %q is missing from table %q", contract.Lookup.Value, contract.Lookup.Table)
		}
	}
	return nil
}

func columnNamed(table TableSchema, name string) (Column, bool) {
	for _, column := range table.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return Column{}, false
}

func stableKey(table TableSchema, keys []string) bool {
	if len(keys) == 1 {
		column, ok := columnNamed(table, keys[0])
		indexedPrimary := slices.ContainsFunc(table.Indexes, func(index Index) bool { return index.Origin == "pk" })
		if ok && column.PrimaryKey == 1 && strings.EqualFold(column.Type, "INTEGER") && !indexedPrimary {
			return true // INTEGER PRIMARY KEY is a rowid alias with implicit non-null uniqueness.
		}
	}
	for _, key := range keys {
		column, ok := columnNamed(table, key)
		if !ok || !column.NotNull || column.Hidden != 0 {
			return false
		}
	}
	for _, index := range table.Indexes {
		if !index.Unique || index.Partial || len(index.Columns) != len(keys) {
			continue
		}
		match := true
		for _, key := range keys {
			match = match && slices.ContainsFunc(index.Columns, func(column IndexColumn) bool {
				return column.Name != nil && *column.Name == key && strings.EqualFold(column.Collation, "BINARY")
			})
		}
		if match {
			return true
		}
	}
	return false
}
