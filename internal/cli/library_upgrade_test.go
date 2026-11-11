package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func TestLibraryPackageSchemaUpgrade(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	contract := librarydb.ExampleContract()
	pkg := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "a32836ef-9ce5-43d3-a854-7a717aaec906", Type: "library",
		Name: "sbmc-library-schema-fixture", Publisher: "sbmc", ShortName: "schema-fixture", DisplayName: "结构迁移示例库",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json", Data: "fixture.library.db",
		Library: &contract,
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, pkg))
	_, before := installedLibrary(t, home, pkg.ID)
	libraryExec(t, before.work, "UPDATE records SET value='local' WHERE key='DEMO01'")
	oldData, err := os.ReadFile(before.work)
	if err != nil {
		t.Fatal(err)
	}
	contract.SchemaVersion = 2
	pkg.Version = "2.0.0"
	makeRelease := func() string {
		root := libraryFixture(t, pkg)
		librarySnapshot(t, filepath.Join(root, pkg.Data), "new package", "2", true)
		return root
	}
	libraryCLI(t, 1, "", "plugin", "install", "--file", makeRelease())
	if out := libraryCLI(t, 0, "", "library", pkg.ShortName); !strings.Contains(out, "1.0.0") {
		t.Fatalf("incompatible package changed active version: %s", out)
	}
	pkg.LibraryMigrations = map[string]string{"1": "migrate.sql"}
	upgrade := makeRelease()
	if err := os.WriteFile(filepath.Join(upgrade, "migrate.sql"), []byte("ALTER TABLE records ADD COLUMN note TEXT;"), 0600); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", upgrade)
	_, after := installedLibrary(t, home, pkg.ID)
	if value, err := librarydb.Query(after.work, pkg.ID, contract, []string{"DEMO01"}); err != nil || value != "local" {
		t.Fatalf("migrated working value = %q, %v", value, err)
	}
	if current, err := os.ReadFile(before.work); err != nil || !bytes.Equal(current, oldData) {
		t.Fatalf("original working database was changed: %v", err)
	}
}

func archiveUpgradeFixture(t *testing.T, dir string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	if err := z.AddFS(os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func upgradeToolFixture(t *testing.T, manifest packages.Manifest) string {
	t.Helper()
	root := t.TempDir()
	manifest.Provides.Tools = map[string]packages.Tool{
		"fixture_upgrade_tool": {Entry: "tool.sh", Scope: "global", Runtime: "bash"},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string][]byte{
		packages.ManifestFile: data, manifest.Help: []byte("{}"), manifest.Changelog: []byte("{}"),
		"tool.sh": []byte("#!/usr/bin/env bash\nprintf '%s\\n' 'fixture'\n"),
	} {
		if err := os.WriteFile(filepath.Join(root, path), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
