package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func recoveryLibraryManifest() packages.Manifest {
	contract := librarydb.ExampleContract()
	return packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "41eec55b-3b98-46ae-a559-cf733ade02ce", Type: "library",
		Name: "sbmc-library-recovery-fixture", Publisher: "sbmc", ShortName: "recovery-fixture", DisplayName: "恢复示例",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json", Data: "fixture.library.db", Library: &contract,
	}
}

func TestLibraryReinstallMergesLastWorkingVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	manifest := recoveryLibraryManifest()
	initial := libraryFixture(t, manifest)
	libraryCLI(t, 0, "", "plugin", "install", "--file", initial)
	a, first := installedLibrary(t, home, manifest.ID)
	edit := func(value string) {
		t.Helper()
		p, err := a.store.Find(manifest.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.mutateLibrary(p, librarydb.RecordEdit{
			Table: "records", Action: "update", Key: []any{"DEMO01"}, Values: map[string]any{"value": value},
		}, "manual", nil); err != nil {
			t.Fatal(err)
		}
	}
	edit("first local")
	_, edited := installedLibrary(t, home, manifest.ID)
	if edited.registered != first.registered {
		t.Fatal("record edit created a new generation")
	}

	manifest.Version = "1.0.1"
	incoming := libraryFixture(t, manifest)
	libraryExec(t, filepath.Join(incoming, manifest.Data), "UPDATE records SET value='new seed' WHERE key='DEMO01'")
	libraryCLI(t, 0, "", "plugin", "remove", manifest.Name)
	libraryCLI(t, 0, "", "plugin", "install", "--file", incoming)
	_, second := installedLibrary(t, home, manifest.ID)
	if value, err := librarydb.Query(second.work, manifest.ID, *manifest.Library, []string{"DEMO01"}); err != nil || value != "first local" {
		t.Fatalf("reinstall = %q, %v", value, err)
	}
	if value, err := librarydb.Query(second.baseline, manifest.ID, *manifest.Library, []string{"DEMO01"}); err != nil || value != "new seed" {
		t.Fatalf("reinstall baseline = %q, %v", value, err)
	}

	edit("latest local")
	libraryCLI(t, 0, "", "plugin", "remove", manifest.Name)
	libraryCLI(t, 0, "", "plugin", "install", "--file", initial)
	_, older := installedLibrary(t, home, manifest.ID)
	if value, err := librarydb.Query(older.work, manifest.ID, *manifest.Library, []string{"DEMO01"}); err != nil || value != "latest local" {
		t.Fatalf("older release reinstall = %q, %v", value, err)
	}
	audit, err := os.ReadFile(older.audit)
	if err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(audit, []byte{'\n'}); lines != 5 {
		t.Fatalf("audit should carry all edits and upgrades, got %d lines", lines)
	}
}

func TestLibraryAuditRecoversInterruptedProjection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	manifest := recoveryLibraryManifest()
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, manifest))
	a, state := installedLibrary(t, home, manifest.ID)
	p, err := a.store.Find(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(state.baseline)
	if err != nil {
		t.Fatal(err)
	}
	for i, mode := range []string{"partial", "already written"} {
		edit := librarydb.RecordEdit{Table: "records", Action: "update", Key: []any{"DEMO01"}, Values: map[string]any{"value": mode}}
		if err := editWorkingLibrary(p, state, edit, "manual"); err != nil {
			t.Fatal(err)
		}
		if value, err := librarydb.Query(state.work, p.ID, *p.Library, []string{"DEMO01"}); err != nil || value != mode {
			t.Fatalf("working data = %q, %v", value, err)
		}
		pending, err := librarydb.PendingAudit(state.work)
		if err != nil || pending == nil {
			t.Fatalf("pending audit = %v, %v", pending, err)
		}
		file, err := os.OpenFile(state.audit, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		line := pending.Line
		if i == 0 {
			line = line[:len(line)/2]
		}
		_, writeErr := file.Write(line)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			t.Fatal(err)
		}
		libraryCLI(t, 0, "", "library", manifest.ShortName)
		if pending, err := librarydb.PendingAudit(state.work); err != nil || pending != nil {
			t.Fatalf("pending audit after retry = %v, %v", pending, err)
		}
		if _, err := librarydb.Validate(state.work, p.ID, *p.Library); err != nil {
			t.Fatalf("final working schema should remain readable by previous core: %v", err)
		}
		audit, err := os.ReadFile(state.audit)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(audit), []byte{'\n'})
		if len(lines) != i+2 {
			t.Fatalf("audit should have %d lines, got %d", i+2, len(lines))
		}
		var entry libraryAuditEntry
		if err := json.Unmarshal(lines[len(lines)-1], &entry); err != nil || entry.Action != "update" {
			t.Fatalf("recovered audit entry = %v, %v", entry, err)
		}
	}
	_, after := installedLibrary(t, home, manifest.ID)
	afterBaseline, err := os.ReadFile(after.baseline)
	if err != nil || !bytes.Equal(baseline, afterBaseline) || after.registered != state.registered {
		t.Fatal("audited edits changed baseline or working generation")
	}
}

func TestLibraryConflictPreviewRedacts(t *testing.T) {
	contract := librarydb.ExampleContract()
	contract.Tables[0].SensitiveFields = []string{"key", "pin"}
	plan := librarydb.Plan{Changes: []librarydb.Change{{
		Table: "records", Key: []any{"private-key-content"}, Local: []any{"present"}, Remote: []any{"present"},
		Fields: []librarydb.FieldChange{
			{Name: "value", Local: "safe-local", Remote: "safe-remote", Conflict: true},
			{Name: "pin", Local: "secret-pin-local", Remote: "secret-pin-remote", Conflict: true},
			{Name: "password", Local: "secret-password-local", Remote: "secret-password-remote", Conflict: true},
		},
	}}}
	var output bytes.Buffer
	a := &app{out: &output}
	changes := a.reviewLibraryMerge(&plan, bufio.NewReader(strings.NewReader("r\nl\nl\n")), true, contract)
	report := output.String()
	for _, want := range []string{"safe-local", "safe-remote", "[已脱敏]"} {
		if !strings.Contains(report, want) {
			t.Fatalf("missing %q from conflict preview: %s", want, report)
		}
	}
	for _, secret := range []string{"secret-", "private-key-content"} {
		if strings.Contains(report, secret) {
			t.Fatalf("conflict preview exposed %q", secret)
		}
	}
	if len(changes) != 3 || changes[0].Selected != "package" || changes[1].Selected != "local" || changes[0].Key[0] != "[已脱敏]" {
		t.Fatalf("unexpected conflict audit: %+v", changes)
	}
}

func TestLibraryMigratesLegacyJSONWithoutDiscardingRecords(t *testing.T) {
	for _, columns := range [][2]string{{"key", "value"}, {"ID", "value"}, {"ID", "brief"}} {
		t.Run(columns[0]+"-"+columns[1], func(t *testing.T) {
			testLibraryMigratesLegacyJSON(t, columns[0], columns[1])
		})
	}
}

func testLibraryMigratesLegacyJSON(t *testing.T, keyColumn, valueColumn string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	manifest := recoveryLibraryManifest()
	sqlite := manifest
	manifest.Library = nil
	manifest.Data = "records.json"
	legacy := libraryFixture(t, manifest)
	if err := os.WriteFile(filepath.Join(legacy, manifest.Data), []byte(`[
		{"key":"DEMO01","value":"legacy edit"},
		{"key":"0001","value":"legacy only"}
	]`), 0600); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", legacy)
	sqlite.Version = "1.0.1"
	sqlite.Library.Tables[0].Keys = []string{keyColumn}
	sqlite.Library.Lookup.Value = valueColumn
	incoming := libraryFixture(t, sqlite)
	dbPath := filepath.Join(incoming, sqlite.Data)
	key := librarydb.QuoteIdentifier(keyColumn)
	value := librarydb.QuoteIdentifier(valueColumn)
	if keyColumn != "key" {
		libraryExec(t, dbPath, "ALTER TABLE records RENAME COLUMN key TO "+key)
	}
	if valueColumn != "value" {
		libraryExec(t, dbPath, "ALTER TABLE records RENAME COLUMN value TO "+value)
	}
	libraryExec(t, dbPath, "INSERT INTO records ("+key+","+value+") VALUES ('NEW','incoming')")
	libraryCLI(t, 0, "", "plugin", "install", "--file", incoming)
	_, state := installedLibrary(t, home, manifest.ID)
	for key, expected := range map[string]string{"DEMO01": "legacy edit", "0001": "legacy only", "NEW": "incoming"} {
		if value, err := librarydb.Query(state.work, manifest.ID, *sqlite.Library, []string{key}); err != nil || value != expected {
			t.Fatalf("%s = %q, %v, want %q", key, value, err, expected)
		}
	}
}
