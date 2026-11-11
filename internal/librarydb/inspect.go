package librarydb

import (
	"database/sql"
	"errors"
	"fmt"
	"maps"
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
	if immutable {
		var exists bool
		if err := tx.QueryRow("SELECT EXISTS (SELECT 1 FROM sqlite_schema WHERE name = ? COLLATE NOCASE)", pendingAuditTable).Scan(&exists); err != nil {
			return Snapshot{}, err
		}
		if exists {
			return Snapshot{}, errors.New("业务库发布数据库含有本地工作库的审计状态")
		}
	}
	return inspect(tx, id)
}

func quickCheck(tx *sql.Tx) error {
	var check string
	if err := tx.QueryRow("PRAGMA quick_check(1)").Scan(&check); err != nil {
		return fmt.Errorf("SQLite 快速检查失败：%w", err)
	}
	if check != "ok" {
		return errors.New("业务库数据库未通过 SQLite 快速检查")
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
		WHERE t.schema = 'main' AND t.type = 'table' AND s.type = 'table' AND t.name NOT IN (?, ?)
		AND substr(t.name, 1, 7) <> 'sqlite_' ORDER BY t.name`, metadataTable, pendingAuditTable)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Tables = make(map[string]TableSchema, len(tables))
	for _, table := range tables {
		if err := inspectTable(tx, &table); err != nil {
			return Snapshot{}, fmt.Errorf("业务库表 %q：%w", table.Name, err)
		}
		snapshot.Tables[table.Name] = table
	}
	return snapshot, nil
}

func readIdentity(tx *sql.Tx, id string) (Snapshot, error) {
	var kind string
	if err := tx.QueryRow(`SELECT type FROM pragma_table_list WHERE schema = 'main' AND name = ?`, metadataTable).Scan(&kind); err != nil || kind != "table" {
		return Snapshot{}, fmt.Errorf("业务库元数据 %q 须为普通数据表", metadataTable)
	}
	entries, err := queryRows(tx, func(rows *sql.Rows) (Snapshot, error) {
		var meta Snapshot
		err := rows.Scan(&meta.ID, &meta.SchemaVersion, &meta.DataVersion)
		return meta, err
	}, `SELECT id, schema_version, data_version FROM main._bmcshell_library LIMIT 2`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("无法读取业务库元数据：%w", err)
	}
	if len(entries) != 1 || !libraryID.MatchString(entries[0].ID) || entries[0].ID != id ||
		!validIdentifier(entries[0].DataVersion) || len(entries[0].DataVersion) > 128 {
		return Snapshot{}, errors.New("业务库元数据须恰好有一条记录，包含匹配的包 ID 和有效的 dataVersion")
	}
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return Snapshot{}, err
	}
	if version < 1 || version != entries[0].SchemaVersion {
		return Snapshot{}, errors.New("业务库的 schemaVersion 须与 SQLite 的 user_version 一致")
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
		return Snapshot{}, errors.New("业务库数据文件须以 .library.db 结尾")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return Snapshot{}, fmt.Errorf("业务库发布数据库须为独立文件，发现附属文件 %s", suffix)
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

// ValidateWorking checks a live working Library, including unprojected audit state.
func ValidateWorking(path, id string, contract Contract) (Snapshot, error) {
	snapshot, err := Inspect(path, id)
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
		return fmt.Errorf("业务库结构版本 %d 与所需版本 %d 不一致", snapshot.SchemaVersion, contract.SchemaVersion)
	}
	for _, declared := range contract.Tables {
		table, exists := snapshot.Tables[declared.Name]
		if !exists {
			return fmt.Errorf("业务库表 %q 不存在或不是普通数据表", declared.Name)
		}
		for _, field := range declared.SensitiveFields {
			if _, ok := columnNamed(table, field); !ok {
				return fmt.Errorf("业务库敏感字段 %q 在表 %q 中不存在", field, table.Name)
			}
		}
		for _, field := range declared.ViewColumns {
			if column, ok := columnNamed(table, field); !ok || column.Hidden == 1 {
				return fmt.Errorf("业务库显示列 %q 在表 %q 中不存在", field, table.Name)
			}
		}
		for _, field := range slices.Sorted(maps.Keys(declared.Fields)) {
			if column, ok := columnNamed(table, field); !ok || column.Hidden == 1 {
				return fmt.Errorf("业务库字段 %q 在表 %q 中不存在", field, table.Name)
			}
		}
		if !stableKey(table, declared.Keys) {
			return fmt.Errorf("业务库表 %q 的 %s 须为非空主键或使用二进制排序规则的唯一键", declared.Name, strings.Join(declared.Keys, ", "))
		}
	}
	if contract.Lookup != (Lookup{}) {
		if _, ok := columnNamed(snapshot.Tables[contract.Lookup.Table], contract.Lookup.Value); !ok {
			return fmt.Errorf("业务库查询列 %q 在表 %q 中不存在", contract.Lookup.Value, contract.Lookup.Table)
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
