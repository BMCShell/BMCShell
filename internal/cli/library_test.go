package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func TestLibraryPackageUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input       string
		interactive bool
		value       string
		deleted     bool
	}{
		{"unattended", "", false, "local", false},
		{"interactive", "r\ny\n", true, "package", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("BMCSHELL_HOME", home)
			contract := librarydb.ExampleContract()
			pkg := packages.Manifest{
				Schema: 1, CoreAPI: 1, ID: "93ca11e1-1c9a-4dd9-8f3e-cf6fa6353851", Type: "library",
				Name: "sbmc-library-upgrade-fixture", Publisher: "sbmc", ShortName: "upgrade-fixture", DisplayName: "增量升级示例库",
				Version: "1.0.0", Help: "help.json", Changelog: "changelog.json", Data: "fixture.library.db",
				Library: &contract,
			}
			initial := libraryFixture(t, pkg)
			libraryExec(t, filepath.Join(initial, pkg.Data), `ALTER TABLE records ADD COLUMN note TEXT;
				ALTER TABLE records ADD COLUMN label TEXT; INSERT INTO records (key,value) VALUES ('REMOVE','base');`)
			libraryCLI(t, 0, "", "plugin", "install", "--file", initial)
			a, before := installedLibrary(t, home, pkg.ID)
			if before.registered == "" || before.work == before.baseline {
				t.Fatalf("missing independent work and baseline: %+v", before)
			}
			libraryExec(t, before.work, `UPDATE records SET value='local', note='local-only' WHERE key='DEMO01';
				UPDATE records SET value='locally edited' WHERE key='REMOVE';
				INSERT INTO records (key,value) VALUES ('LOCAL','keep');`)
			libraryCLI(t, 0, "", "plugin", "install", "--file", initial)
			if out := libraryCLI(t, 0, "", "library", pkg.ShortName); !strings.Contains(out, pkg.Version) {
				t.Fatalf("unexpected Library information: %q", out)
			}

			pkg.Version = "2.0.0"
			incoming := libraryFixture(t, pkg)
			libraryExec(t, filepath.Join(incoming, pkg.Data), `ALTER TABLE records ADD COLUMN note TEXT;
				ALTER TABLE records ADD COLUMN label TEXT;
				UPDATE records SET value='package', label='package-only' WHERE key='DEMO01';
				UPDATE _bmcshell_library SET data_version='2';
				INSERT INTO records (key,value) VALUES ('NEW','new');`)
			var output bytes.Buffer
			a.out = &output
			if err := a.installLocalPackage(incoming, bufio.NewReader(strings.NewReader(tc.input)), tc.interactive); err != nil {
				t.Fatal(err)
			}
			for _, message := range []string{"字段冲突", "拟删除", "本地记录已修改", "新增", "修改"} {
				if !strings.Contains(output.String(), message) {
					t.Fatalf("missing %q in report: %s", message, &output)
				}
			}
			if generation, err := a.store.LibraryGeneration(pkg.ID, "1.0.0"); err != nil || generation != before.registered {
				t.Fatalf("retained version generation = %q, %v, want %q", generation, err, before.registered)
			}
			_, after := installedLibrary(t, home, pkg.ID)
			db, err := librarydb.OpenReadOnly(after.work)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var value, note, label string
			if err := db.QueryRow("SELECT value,note,label FROM records WHERE key='DEMO01'").Scan(&value, &note, &label); err != nil {
				t.Fatal(err)
			}
			if value != tc.value || note != "local-only" || label != "package-only" {
				t.Fatalf("merged record = %q %q %q", value, note, label)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM records WHERE key IN ('LOCAL','NEW')").Scan(&count); err != nil || count != 2 {
				t.Fatalf("additions missing: %d, %v", count, err)
			}
			if err := db.QueryRow("SELECT count(*) FROM records WHERE key='REMOVE'").Scan(&count); err != nil || (count == 0) != tc.deleted {
				t.Fatalf("deletion result: %d, %v", count, err)
			}
			audit, err := os.ReadFile(after.audit)
			if err != nil {
				t.Fatal(err)
			}
			var first, last libraryAuditEntry
			dec := json.NewDecoder(bytes.NewReader(audit))
			if err := dec.Decode(&first); err != nil {
				t.Fatal(err)
			}
			if err := dec.Decode(&last); err != nil {
				t.Fatal(err)
			}
			if first.Action != "install" || last.Action != "upgrade" || last.Source != "upgrade" {
				t.Fatalf("unexpected audit: %s", audit)
			}
			for _, change := range last.Changes {
				if change.Field == "value" && change.Conflict && change.Selected == tc.value {
					return
				}
			}
			t.Fatalf("missing selected conflict in audit: %s", audit)
		})
	}
}

func TestLegacyLibraryInfoCLI(t *testing.T) {
	t.Setenv("BMCSHELL_HOME", t.TempDir())
	pkg := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "807d99b2-7d71-4c47-9908-3c40826410f0", Type: "library",
		Name: "旧版示例库", Summary: "旧版 JSON 记录",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json", Data: "records.json",
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, pkg))
	if out := libraryCLI(t, 0, "", "library", pkg.Name); !strings.Contains(out, "旧版 JSON 数据") {
		t.Fatalf("legacy info = %q", out)
	}
}

func TestLegacyLibraryGenerationImport(t *testing.T) {
	for _, removed := range []bool{false, true} {
		name := "installed"
		if removed {
			name = "after removal"
		}
		t.Run(name, func(t *testing.T) {
			configDir := t.TempDir()
			t.Setenv("BMCSHELL_HOME", configDir)
			contract := librarydb.ExampleContract()
			manifest := packages.Manifest{
				Schema: 1, CoreAPI: 1, ID: "55bed6f3-cdd9-41d3-9c8f-81dc6d5e6d70", Type: "library",
				Name: "sbmc-library-legacy-generation", Publisher: "sbmc", ShortName: "legacy-generation",
				DisplayName: "旧目录布局测试库", Version: "1.0.0", Help: "help.json",
				Changelog: "changelog.json", Data: "fixture.library.db", Library: &contract,
			}
			pkgDir := libraryFixture(t, manifest)
			if !removed {
				// Reproduce dev.4's original package directory without a new registry selection.
				legacyPackage := filepath.Join(configDir, "packages", manifest.ID)
				if err := os.MkdirAll(legacyPackage, 0700); err != nil {
					t.Fatal(err)
				}
				for _, file := range []string{packages.ManifestFile, manifest.Help, manifest.Changelog, manifest.Data} {
					if err := copyLibraryFile(filepath.Join(pkgDir, file), filepath.Join(legacyPackage, file)); err != nil {
						t.Fatal(err)
					}
				}
			}
			libraryRoot := filepath.Join(configDir, "libraries", manifest.ID)
			legacyRoot := filepath.Join(libraryRoot, manifest.Version)
			legacyGeneration := filepath.Join(legacyRoot, "gen-123")
			if err := os.MkdirAll(legacyGeneration, 0700); err != nil {
				t.Fatal(err)
			}
			legacyWork := filepath.Join(legacyGeneration, "work.library.db")
			legacyBaseline := filepath.Join(legacyGeneration, "baseline.library.db")
			for _, file := range []string{legacyWork, legacyBaseline} {
				if err := copyLibraryFile(filepath.Join(pkgDir, manifest.Data), file); err != nil {
					t.Fatal(err)
				}
			}
			libraryExec(t, legacyWork, "UPDATE records SET value='retained edit' WHERE key='DEMO01'")
			legacyAudit := []byte("{\"source\":\"manual\",\"action\":\"update\",\"version\":\"1.0.0\"}\n")
			if err := os.WriteFile(filepath.Join(legacyGeneration, "audit.jsonl"), legacyAudit, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(legacyRoot, "active"), []byte("gen-123\n"), 0600); err != nil {
				t.Fatal(err)
			}

			if removed {
				libraryCLI(t, 0, "", "plugin", "install", "--file", pkgDir)
			} else {
				libraryCLI(t, 0, "", "library", manifest.ShortName)
			}
			_, imported := installedLibrary(t, configDir, manifest.ID)
			if imported.registered == "" || filepath.Dir(imported.generation) != libraryRoot || imported.generation == legacyGeneration {
				t.Fatalf("legacy Library was not imported into flat storage: %+v", imported)
			}
			if work, err := librarydb.Query(imported.work, manifest.ID, contract, []string{"DEMO01"}); err != nil || work != "retained edit" {
				t.Fatalf("working data = %q, %v", work, err)
			}
			if baseline, err := librarydb.Query(imported.baseline, manifest.ID, contract, []string{"DEMO01"}); err != nil || baseline != "示例设备 A" {
				t.Fatalf("baseline data = %q, %v", baseline, err)
			}
			if audit, err := os.ReadFile(imported.audit); err != nil || !bytes.Equal(audit, legacyAudit) {
				t.Fatalf("imported audit = %q, %v", audit, err)
			}
			if marker, err := os.ReadFile(filepath.Join(legacyRoot, "active")); err != nil || string(marker) != "gen-123\n" {
				t.Fatalf("legacy marker = %q, %v", marker, err)
			}

			libraryCLI(t, 0, "", "plugin", "remove", manifest.Name)
			libraryCLI(t, 0, "", "plugin", "install", "--file", pkgDir)
			_, reinstalled := installedLibrary(t, configDir, manifest.ID)
			if reinstalled.registered != imported.registered {
				t.Fatalf("reinstall lost retained generation: %q, want %q", reinstalled.registered, imported.registered)
			}
		})
	}
}

func TestLibraryMenuAndAuditSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	contract := librarydb.ExampleContract()
	manifest := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "cbe1d63d-9823-4e12-afac-c99f8992c373", Type: "library",
		Name: "sbmc-library-menu-fixture", Publisher: "sbmc", ShortName: "menu-fixture", DisplayName: "交互管理示例库",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json",
		Data: "fixture.library.db", Library: &contract,
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, manifest))
	a, _ := installedLibrary(t, home, manifest.ID)
	p, err := a.store.Find(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	a.out, a.errOut = &out, &errs
	input := bufio.NewReader(strings.NewReader("1\nDEMO01\n2\nNEW\nmanual\n3\nNEW\nmodified\n4\nDEMO01\ny\n0\n"))
	if err := a.libraryMenu(p, input); err != nil || errs.Len() != 0 {
		t.Fatalf("menu failed: %v\n%s\n%s", err, &out, &errs)
	}
	if strings.Count(out.String(), "已保存。") != 3 {
		t.Fatalf("unexpected menu output: %s", &out)
	}
	_, state := installedLibrary(t, home, p.ID)
	if value, err := librarydb.Query(state.work, p.ID, contract, []string{"NEW"}); err != nil || value != "modified" {
		t.Fatalf("manual result = %q, %v", value, err)
	}
	if _, err := librarydb.Query(state.work, p.ID, contract, []string{"DEMO01"}); err == nil {
		t.Fatal("menu did not delete the confirmed record")
	}
	edit := librarydb.RecordEdit{
		Action: "update", Table: "records", Key: []any{"NEW"}, Values: map[string]any{"value": "plugin"},
	}
	if err := a.mutateLibrary(p, edit, "plugin", nil); err != nil {
		t.Fatal(err)
	}
	_, after := installedLibrary(t, home, p.ID)
	if value, err := librarydb.Query(after.work, p.ID, contract, []string{"NEW"}); err != nil || value != "plugin" {
		t.Fatalf("plugin result = %q, %v", value, err)
	}
	audit, err := os.ReadFile(after.audit)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(audit))
	for _, want := range []struct{ source, action string }{
		{"upgrade", "install"}, {"manual", "add"}, {"manual", "update"}, {"manual", "delete"}, {"plugin", "update"},
	} {
		var entry libraryAuditEntry
		if err := dec.Decode(&entry); err != nil || entry.Source != want.source || entry.Action != want.action {
			t.Fatalf("audit entry = %+v, %v, want %+v", entry, err, want)
		}
	}
}

func installedLibrary(t *testing.T, home, id string) (*app, libraryFiles) {
	t.Helper()
	store, err := packages.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{home: home, store: store}
	files, err := a.libraryFiles(p)
	if err != nil {
		t.Fatal(err)
	}
	return a, files
}

func libraryExec(t *testing.T, path, query string) {
	t.Helper()
	db, err := librarydb.OpenReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func libraryCLI(t *testing.T, want int, input string, args ...string) string {
	t.Helper()
	var out, errs bytes.Buffer
	if got := Run("test", args, strings.NewReader(input), &out, &errs); got != want {
		t.Fatalf("%v exit status = %d, want %d\nstdout: %s\nstderr: %s", args, got, want, &out, &errs)
	}
	return out.String()
}

func libraryFixture(t *testing.T, pkg packages.Manifest) string {
	t.Helper()
	root := t.TempDir()
	manifest, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		packages.ManifestFile: manifest, pkg.Help: []byte(`{}`), pkg.Changelog: []byte(`{}`),
	}
	if pkg.Library == nil {
		files[pkg.Data] = []byte(`[{"key":"DEMO01","value":"旧版设备"}]`)
	} else if err := librarydb.CreateExample(filepath.Join(root, pkg.Data), pkg.ID); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func librarySnapshot(t *testing.T, path, value, version string, changeSchema bool) []byte {
	t.Helper()
	db, err := librarydb.OpenReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE records SET value = ? WHERE key = 'DEMO01'`, value); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE _bmcshell_library SET data_version = ?`, version); err != nil {
		t.Fatal(err)
	}
	if changeSchema {
		if _, err := db.Exec(`ALTER TABLE records ADD COLUMN note TEXT;
			UPDATE _bmcshell_library SET schema_version = 2;
			PRAGMA user_version = 2;`); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
