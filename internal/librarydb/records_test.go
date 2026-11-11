package librarydb

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRecordEditsValidateStoredValuesBeforeAudit(t *testing.T) {
	path := exampleFile(t)
	execFixture(t, path, `
CREATE TABLE devices (
  device_id INTEGER PRIMARY KEY,
  display_name TEXT DEFAULT '',
  status TEXT DEFAULT 'ready',
  observed DATETIME,
  enabled BOOLEAN DEFAULT 1
);
INSERT INTO devices (device_id) VALUES (1);
CREATE TRIGGER invalidate_name AFTER INSERT ON devices WHEN new.display_name = 'trigger'
BEGIN
  UPDATE devices SET display_name = '' WHERE device_id = new.device_id;
END;
`)
	var contract Contract
	err := json.Unmarshal([]byte(`{
		"schemaVersion": 1,
		"tables": [{
			"name": "devices",
			"keys": ["device_id"],
			"fields": {
				"device_id": {"type": "integer", "max": 9007199254740993},
				"display_name": {"type": "text", "required": true, "pattern": "^.+$", "maxBytes": 7},
				"status": {"type": "text", "enum": ["ready", "offline"]},
				"observed": {"type": "text", "pattern": "^2026"},
				"enabled": {"type": "integer", "min": 0, "max": 1}
			}
		}]
	}`), &contract)
	if err != nil {
		t.Fatal(err)
	}
	audits := 0
	edit := func(action string, key []any, values map[string]any) error {
		return EditRecord(path, testID, contract, RecordEdit{Action: action, Table: "devices", Key: key, Values: values}, 0,
			func([]any, []string) ([]byte, error) {
				audits++
				return []byte("{}\n"), nil
			})
	}
	for _, attempt := range []struct {
		values map[string]any
		field  string
	}{
		{nil, "display_name"},
		{map[string]any{"display_name": "trigger"}, "display_name"},
		{map[string]any{"display_name": "设备名"}, "display_name"},
		{map[string]any{"display_name": "device", "device_id": int64(9007199254740994)}, "device_id"},
	} {
		err := edit("add", nil, attempt.values)
		if err == nil || !strings.Contains(err.Error(), attempt.field) {
			t.Fatalf("add with invalid %s returned %v", attempt.field, err)
		}
	}
	if err := edit("update", []any{1}, map[string]any{"status": "bad status"}); err == nil {
		t.Fatal("accepted value outside the enum")
	}
	_, rows, err := ReadRecords(path, testID, contract, "devices", nil, 10)
	if err != nil || len(rows) != 1 || rows[0][2] != "ready" || audits != 0 {
		t.Fatalf("invalid edits affected rows or audit: %v, %v, %d audit(s)", rows, err, audits)
	}
	if err := edit("update", []any{1}, map[string]any{"status": "offline"}); err != nil {
		t.Fatalf("unrelated field on an existing invalid record: %v", err)
	}
	pending, err := PendingAudit(path)
	if err != nil || pending == nil {
		t.Fatalf("missing update audit: %v", err)
	}
	if err := AcknowledgeAudit(path, *pending); err != nil {
		t.Fatal(err)
	}
	if err := edit("add", nil, map[string]any{
		"device_id": "9007199254740993", "display_name": "device", "observed": "2026-11-11T10:00:00Z",
	}); err != nil {
		t.Fatalf("valid SQLite-coerced values and defaults: %v", err)
	}
}

func TestRecordViewUsesMetadataForProjectionAndSearch(t *testing.T) {
	path := exampleFile(t)
	execFixture(t, path, `
CREATE TABLE devices (
  device_id INTEGER PRIMARY KEY,
  display_name TEXT NOT NULL,
  password TEXT NOT NULL,
  payload BLOB
);
INSERT INTO devices VALUES (10, 'test', '% hidden', X'6100'), (2, 'test %', 'hidden', NULL), (3, 'test', 'hidden', NULL);
`)
	contract := Contract{SchemaVersion: 1, Tables: []Table{{
		Name: "devices", Keys: []string{"device_id"}, ViewColumns: []string{"display_name", "device_id", "payload"},
	}}}
	columns, err := ViewColumns(path, testID, contract, "devices")
	if err != nil || !reflect.DeepEqual(columns, []string{"display_name", "device_id", "payload"}) {
		t.Fatalf("view columns: %v, %v", columns, err)
	}
	for _, tc := range []struct {
		view RecordView
		want [][]any
	}{
		{RecordView{Search: "%", Limit: 5}, [][]any{{"test %", int64(2), nil}}},
		{RecordView{Field: "display_name", Search: "test", Offset: 1, Limit: 1}, [][]any{{"test", int64(3), nil}}},
		{RecordView{Field: "payload", Search: "hex:6100", Limit: 5}, [][]any{{"test", int64(10), []byte{0x61, 0x00}}}},
		{RecordView{Search: "hidden", Limit: 5}, nil},
	} {
		headers, rows, err := ViewRecords(path, testID, contract, "devices", tc.view)
		if err != nil || !reflect.DeepEqual(headers, columns) || !reflect.DeepEqual(rows, tc.want) {
			t.Errorf("view %+v returned %v, %v, %v", tc.view, headers, rows, err)
		}
	}
	if _, _, err := ViewRecords(path, testID, contract, "devices", RecordView{Field: "password", Search: "hidden", Limit: 5}); err == nil {
		t.Fatal("allowed search of a sensitive field")
	}
}
