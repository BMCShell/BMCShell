package librarydb

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const testID = "d68b6159-648e-425c-ac57-f800aa71b920"

func TestExampleLookupAndIdentity(t *testing.T) {
	path := exampleFile(t)
	contract := ExampleContract()
	snapshot, err := Validate(path, testID, contract)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != testID || snapshot.SchemaVersion != 1 || snapshot.DataVersion != "1" || len(snapshot.Tables) != 1 {
		t.Fatalf("unexpected snapshot %+v", snapshot)
	}
	value, err := Query(path, testID, contract, []string{"DEMO01"})
	if err != nil || value != "示例设备 A" {
		t.Fatalf("Query = %q, %v", value, err)
	}
	if _, err := Query(path, testID, contract, []string{"DEMO01' OR 1=1 --"}); err == nil {
		t.Fatal("unexpected match for a SQL-looking key")
	}
	if _, err := Validate(path, "a3da866b-bfc1-4983-9b8a-c97c21adf90f", contract); err == nil {
		t.Fatal("accepted wrong package identity")
	}
	if err := CreateExample(path, testID); err == nil {
		t.Fatal("overwrote existing database")
	}
}

func TestStableKeysAndSchemaDifference(t *testing.T) {
	path := exampleFile(t)
	before, err := Inspect(path, testID)
	if err != nil {
		t.Fatal(err)
	}
	execFixture(t, path, `
ALTER TABLE records ADD COLUMN detail TEXT NOT NULL DEFAULT '';
CREATE TABLE composite (tenant TEXT NOT NULL, id INTEGER NOT NULL, value TEXT NOT NULL, UNIQUE(id, tenant));
INSERT INTO composite (tenant, id, value) VALUES ('example', 2, 'compound key');
CREATE TABLE rowid_key (id INTEGER PRIMARY KEY, value TEXT);
CREATE TABLE nullable_key (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE partial_key (key TEXT NOT NULL, value TEXT);
CREATE UNIQUE INDEX partial_only ON partial_key (key) WHERE value IS NOT NULL;
CREATE TABLE nocase_key (key TEXT NOT NULL COLLATE NOCASE PRIMARY KEY, value TEXT);
`)
	after, err := Inspect(path, testID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SchemaVersion != before.SchemaVersion || reflect.DeepEqual(before.Tables["records"], after.Tables["records"]) {
		t.Fatal("actual column change was missed at the same schema version")
	}
	for _, tc := range []struct {
		table string
		keys  []string
		valid bool
	}{
		{"composite", []string{"tenant", "id"}, true},
		{"rowid_key", []string{"id"}, true},
		{"nullable_key", []string{"key"}, false},
		{"partial_key", []string{"key"}, false},
		{"nocase_key", []string{"key"}, false},
	} {
		contract := Contract{SchemaVersion: 1, Tables: []Table{{Name: tc.table, Keys: tc.keys}}}
		if err := ValidateSnapshot(after, contract); (err == nil) != tc.valid {
			t.Errorf("ValidateSnapshot(%s) = %v", tc.table, err)
		}
	}
	contract := Contract{
		SchemaVersion: 1,
		Tables:        []Table{{Name: "composite", Keys: []string{"tenant", "id"}}},
		Lookup:        Lookup{Table: "composite", Value: "value"},
	}
	value, err := Query(path, testID, contract, []string{"example", "2"})
	if err != nil || value != "compound key" {
		t.Fatalf("Query = %q, %v", value, err)
	}
}

func TestInspectRejectsInvalidSQLiteAndInconsistentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.library.db")
	if err := os.WriteFile(path, []byte("not SQLite"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(path, testID); err == nil {
		t.Fatal("accepted invalid SQLite data")
	}
	path = exampleFile(t)
	execFixture(t, path, `PRAGMA user_version = 2`)
	if _, err := Inspect(path, testID); err == nil {
		t.Fatal("accepted disagreeing schema versions")
	}
}

func TestReleaseValidationRequiresStandaloneDatabase(t *testing.T) {
	path := exampleFile(t)
	execFixture(t, path, `PRAGMA journal_mode = WAL`)
	if _, err := Validate(path, testID, ExampleContract()); err != nil {
		t.Fatalf("checkpointed SQLite file: %v", err)
	}
	if err := os.WriteFile(path+"-wal", []byte("sidecar"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(path, testID, ExampleContract()); err == nil {
		t.Fatal("accepted a database with WAL sidecar")
	}
}

func TestMigrateProtectsTransactionAndVersions(t *testing.T) {
	path := exampleFile(t)
	script := filepath.Join(t.TempDir(), "change.sql")
	for _, unsafe := range []string{
		"ALTER TABLE records ADD COLUMN detail TEXT; COMMIT;",
		"ATTACH DATABASE ':memory:' AS foreign_db;",
		"UPDATE _bmcshell_library SET data_version = 'changed';",
		"CREATE TABLE child (k TEXT REFERENCES records(key)); INSERT INTO child VALUES ('missing');",
	} {
		if err := os.WriteFile(script, []byte(unsafe), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(path, script, 1, 2); err == nil {
			t.Errorf("accepted unsafe migration %q", unsafe)
		}
		snapshot, err := Validate(path, testID, ExampleContract())
		if err != nil || len(snapshot.Tables) != 1 || len(snapshot.Tables["records"].Columns) != 2 || snapshot.DataVersion != "1" {
			t.Fatalf("failed migration changed database: %+v, %v", snapshot, err)
		}
	}
	if err := os.WriteFile(script, []byte("ALTER TABLE records ADD COLUMN detail TEXT NOT NULL DEFAULT '';"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(path, script, 1, 2); err != nil {
		t.Fatal(err)
	}
	contract := ExampleContract()
	contract.SchemaVersion = 2
	snapshot, err := Validate(path, testID, contract)
	if err != nil || snapshot.DataVersion != "1" || len(snapshot.Tables["records"].Columns) != 3 {
		t.Fatalf("migrated snapshot = %+v, %v", snapshot, err)
	}
	if err := Migrate(path, script, 1, 2); err == nil {
		t.Fatal("accepted a stale source version")
	}
}

func exampleFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "example # 100%.library.db")
	if err := CreateExample(path, testID); err != nil {
		t.Fatal(err)
	}
	return path
}

func execFixture(t *testing.T, path, query string) {
	t.Helper()
	db, err := OpenReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}
