package librarydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

// StructureDifferences compares the SQLite definitions as well as the declared schema version.
func StructureDifferences(current, incoming Snapshot) []string {
	var differences []string
	if current.SchemaVersion != incoming.SchemaVersion {
		differences = append(differences, fmt.Sprintf("结构版本 %d → %d", current.SchemaVersion, incoming.SchemaVersion))
	}
	names := make(map[string]bool)
	for name := range current.Tables {
		names[name] = true
	}
	for name := range incoming.Tables {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		before, had := current.Tables[name]
		after, has := incoming.Tables[name]
		switch {
		case !had:
			differences = append(differences, fmt.Sprintf("新增表 %q", name))
		case !has:
			differences = append(differences, fmt.Sprintf("缺少表 %q", name))
		case !reflect.DeepEqual(before, after):
			columns := make(map[string]Column)
			for _, column := range before.Columns {
				columns[column.Name] = column
			}
			var changed []string
			for _, column := range after.Columns {
				if !reflect.DeepEqual(columns[column.Name], column) {
					changed = append(changed, fmt.Sprintf("%q", column.Name))
				}
				delete(columns, column.Name)
			}
			for removed := range columns {
				changed = append(changed, fmt.Sprintf("%q", removed))
			}
			sort.Strings(changed)
			if len(changed) > 0 {
				differences = append(differences, fmt.Sprintf("表 %q 的列发生变化（%s）", name, strings.Join(changed, ", ")))
			} else {
				differences = append(differences, fmt.Sprintf("表 %q 的定义、约束、索引或触发器发生变化", name))
			}
		}
	}
	return differences
}

type Change struct {
	Table                 string
	Key                   []any
	Before, Local, Remote []any
	Conflict              bool
	UseRemote             bool
}

type tableLayout struct {
	columns    []string
	keyIndexes []int
}

type Plan struct {
	Changes         []Change
	BaselineChanged bool
	layouts         map[string]tableLayout
}

// Compare produces a deterministic three-way merge plan. The paths must already
// have been validated against the supplied schema.
func Compare(localPath, baselinePath, remotePath string, snapshot Snapshot, contract Contract, replace bool) (Plan, error) {
	plan := Plan{layouts: make(map[string]tableLayout)}
	local, err := OpenReadOnly(localPath)
	if err != nil {
		return plan, err
	}
	defer local.Close()
	baseline, err := OpenReadOnly(baselinePath)
	if err != nil {
		return plan, err
	}
	defer baseline.Close()
	remote, err := OpenReadOnly(remotePath)
	if err != nil {
		return plan, err
	}
	defer remote.Close()

	for _, table := range contract.Tables {
		var layout tableLayout
		for _, column := range snapshot.Tables[table.Name].Columns {
			if column.Hidden == 0 {
				layout.columns = append(layout.columns, column.Name)
			}
		}
		for _, key := range table.Keys {
			index := slices.Index(layout.columns, key)
			if index < 0 {
				return plan, fmt.Errorf("invalid merge key %s.%s", table.Name, key)
			}
			layout.keyIndexes = append(layout.keyIndexes, index)
		}
		plan.layouts[table.Name] = layout
		localRows, err := readRows(local, table.Name, layout)
		if err != nil {
			return plan, err
		}
		remoteRows, err := readRows(remote, table.Name, layout)
		if err != nil {
			return plan, err
		}
		baselineRows, err := readRows(baseline, table.Name, layout)
		if err != nil {
			return plan, err
		}
		plan.BaselineChanged = plan.BaselineChanged || !reflect.DeepEqual(baselineRows, remoteRows)
		plan.Changes = append(plan.Changes, compareRows(table.Name, layout, localRows, baselineRows, remoteRows, replace)...)
	}
	return plan, nil
}

func readRows(db *sql.DB, table string, layout tableLayout) (map[string][]any, error) {
	rows, err := db.Query("SELECT " + quotedList(layout.columns) + " FROM " + QuoteIdentifier(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]any)
	for rows.Next() {
		if len(result) >= 250000 || (len(result)+1)*len(layout.columns) > 2000000 {
			return nil, fmt.Errorf("table %q exceeds the sync row or cell limit", table)
		}
		record, destinations := make([]any, len(layout.columns)), make([]any, len(layout.columns))
		for i := range record {
			destinations[i] = &record[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		key := mergeKey(record, layout)
		if _, found := result[key]; found {
			return nil, fmt.Errorf("table %s has a duplicate merge key", table)
		}
		result[key] = record
	}
	return result, rows.Err()
}

func mergeKey(row []any, layout tableLayout) string {
	values := keyValues(row, layout)
	for i, value := range values {
		if number, ok := value.(float64); ok && number >= -0x1p63 && number < 0x1p63 {
			if integer := int64(number); float64(integer) == number {
				values[i] = integer
			}
		}
	}
	return fmt.Sprintf("%#v", values)
}

func keyValues(row []any, layout tableLayout) []any {
	values := make([]any, len(layout.keyIndexes))
	for i, index := range layout.keyIndexes {
		values[i] = row[index]
	}
	return values
}

func compareRows(table string, layout tableLayout, local, before, remote map[string][]any, replace bool) []Change {
	names := make(map[string]bool)
	for key := range local {
		names[key] = true
	}
	for key := range remote {
		names[key] = true
	}
	keys := make([]string, 0, len(names))
	for key := range names {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var changes []Change
	for _, key := range keys {
		ours, base, theirs := local[key], before[key], remote[key]
		if reflect.DeepEqual(ours, theirs) || !replace && reflect.DeepEqual(base, theirs) {
			continue
		}
		record := ours
		if record == nil {
			record = theirs
		}
		conflict := !replace && !reflect.DeepEqual(ours, base)
		changes = append(changes, Change{
			Table: table, Key: keyValues(record, layout), Before: base, Local: ours,
			Remote: theirs, Conflict: conflict, UseRemote: !conflict,
		})
	}
	return changes
}

// Apply changes only the staged copy of the working database.
func (p Plan) Apply(path string, dataVersion string) error {
	db, err := OpenReadWrite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Apply exactly the approved changes. Validate referential integrity before
	// commit instead of allowing cascades to modify additional records.
	if err := conn.Raw(func(value any) error {
		connection, ok := value.(driver.Conn)
		if !ok {
			return errors.New("Library sync requires the SQLite driver")
		}
		raw := connection.Raw()
		if _, err := raw.Config(sqlite3.DBCONFIG_ENABLE_TRIGGER, false); err != nil {
			return err
		}
		_, err := raw.Config(sqlite3.DBCONFIG_ENABLE_FKEY, false)
		return err
	}); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, change := range p.Changes {
		if change.UseRemote && change.Local != nil {
			if err := deleteLocalRow(tx, change, p.layouts[change.Table]); err != nil {
				return err
			}
		}
	}
	for _, change := range p.Changes {
		if change.UseRemote && change.Remote != nil {
			if err := insertRemoteRow(tx, change, p.layouts[change.Table]); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec("UPDATE _bmcshell_library SET data_version = ?", dataVersion); err != nil {
		return err
	}
	if err := checkForeignKeys(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteLocalRow(tx *sql.Tx, change Change, layout tableLayout) error {
	var filter []string
	for _, key := range layout.keyIndexes {
		filter = append(filter, QuoteIdentifier(layout.columns[key])+" COLLATE BINARY = ?")
	}
	_, err := tx.Exec("DELETE FROM "+QuoteIdentifier(change.Table)+" WHERE "+strings.Join(filter, " AND "), change.Key...)
	if err != nil {
		return fmt.Errorf("delete from table %q: %w", change.Table, err)
	}
	return nil
}

func insertRemoteRow(tx *sql.Tx, change Change, layout tableLayout) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(layout.columns)), ",")
	_, err := tx.Exec("INSERT INTO "+QuoteIdentifier(change.Table)+" ("+quotedList(layout.columns)+") VALUES ("+placeholders+")", change.Remote...)
	if err != nil {
		return fmt.Errorf("insert into table %q: %w", change.Table, err)
	}
	return nil
}

func quotedList(columns []string) string {
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = QuoteIdentifier(column)
	}
	return strings.Join(names, ", ")
}
