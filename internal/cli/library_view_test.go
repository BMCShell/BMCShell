package cli

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func TestLibraryMenuViewsDeclaredColumns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	contract := librarydb.ExampleContract()
	contract.Tables[0].ViewColumns = []string{"value", "key"}
	contract.Tables[0].Fields = map[string]librarydb.Field{
		"value": {Description: "设备描述"},
	}
	manifest := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "d05ffb4b-4f18-4c80-b29d-47e6511de64b", Type: "library",
		Name: "sbmc-library-view-fixture", Publisher: "sbmc", ShortName: "view-fixture", DisplayName: "记录查看示例库",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json",
		Data: "fixture.library.db", Library: &contract,
	}
	dir := libraryFixture(t, manifest)
	libraryExec(t, filepath.Join(dir, manifest.Data), `ALTER TABLE records ADD COLUMN internal_note TEXT;
		ALTER TABLE records ADD COLUMN password TEXT;
		UPDATE records SET value='shared first', internal_note='private note', password='private password' WHERE key='DEMO01';
		INSERT INTO records (key,value) VALUES ('DEMO03','unrelated'), ('DEMO02','shared second');`)
	libraryCLI(t, 0, "", "plugin", "install", "--file", dir)
	a, _ := installedLibrary(t, home, manifest.ID)
	p, err := a.store.Find(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, action, input, contains, excludes, errContains string
	}{
		{name: "field search and range", action: "1", input: ":search\nvalue\nshared\n2-2\n", contains: "\"shared second\"\t\"DEMO02\"", excludes: "shared first"},
		{name: "all columns search", action: "1", input: ":search\n\nDEMO03\n\n", contains: "\"unrelated\"\t\"DEMO03\"", excludes: "shared"},
		{name: "row range", action: "1", input: ":range\n2-2\n", contains: "\"shared second\"\t\"DEMO02\"", excludes: "shared first"},
		{name: "exact key", action: "1", input: "DEMO01\n", contains: "\"shared first\"\t\"DEMO01\"", excludes: "shared second"},
		{name: "excluded column", action: "1", input: ":search\ninternal_note\n", errContains: "不可搜索表头"},
		{name: "sensitive column", action: "1", input: ":search\npassword\n", errContains: "不可搜索表头"},
		{name: "invalid range", action: "1", input: ":range\n0-1\n", errContains: "无效的记录范围"},
		{name: "update preview and prompt", action: "3", input: "DEMO01\n\n\n\n", contains: "value（设备描述）"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			a.out = &out
			err := a.runLibraryMenuAction(p, p.Library.Tables[0], bufio.NewReader(strings.NewReader(tc.input)), tc.action)
			if tc.errContains == "" && err != nil || tc.errContains != "" && (err == nil || !strings.Contains(err.Error(), tc.errContains)) {
				t.Fatalf("unexpected error: %v", err)
			}
			output := out.String()
			if tc.action == "1" && !strings.HasPrefix(output, "可查看表头\n  value（设备描述）\n  key\n") {
				t.Fatalf("headers must precede viewing prompts: %q", output)
			}
			if !strings.Contains(output, tc.contains) || tc.excludes != "" && strings.Contains(output, tc.excludes) ||
				strings.Contains(output, "private note") || strings.Contains(output, "private password") {
				t.Fatalf("unexpected output: %q", output)
			}
		})
	}
}
