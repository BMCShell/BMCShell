package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func TestLibraryRefreshCLI(t *testing.T) {
	t.Setenv("BMCSHELL_HOME", t.TempDir())
	const name = "sbmc-library-refresh-fixture"
	contract := librarydb.ExampleContract()
	pkg := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "93ca11e1-1c9a-4dd9-8f3e-cf6fa6353851", Type: "library",
		Name: name, Publisher: "sbmc", ShortName: "refresh-fixture", DisplayName: "同步示例库",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json", Data: "fixture.library.db",
		Library: &contract,
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, pkg))

	remote := filepath.Join(t.TempDir(), "remote.library.db")
	if err := librarydb.CreateExample(remote, pkg.ID); err != nil {
		t.Fatal(err)
	}
	var snapshot atomic.Value
	snapshot.Store(librarySnapshot(t, remote, "远端数据 2", "2", false))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write(snapshot.Load().([]byte))
	}))
	defer server.Close()
	libraryCLI(t, 0, "", "library", "source", name, server.URL)

	query := func(want string) {
		t.Helper()
		got := libraryCLI(t, 0, "", "library", "query", name, "DEMO01")
		if strings.TrimSpace(got) != want {
			t.Fatalf("query = %q, want %q", got, want)
		}
	}
	query("示例设备 A")
	if out := libraryCLI(t, 0, "", "library", "refresh", name, "--dry-run"); !strings.Contains(out, "修改 1") {
		t.Fatalf("dry-run did not report the updated record: %q", out)
	}
	query("示例设备 A")
	libraryCLI(t, 0, "n\n", "library", "refresh", name)
	query("示例设备 A")
	if out := libraryCLI(t, 0, "d\ny\n", "library", "refresh", name); !strings.Contains(out, "远端数据 2") {
		t.Fatalf("details did not show the remote value: %q", out)
	}
	query("远端数据 2")

	snapshot.Store(librarySnapshot(t, remote, "远端数据 3", "3", false))
	if out := libraryCLI(t, 0, "y\nn\n", "library", "refresh", name, "--replace-all"); !strings.Contains(out, "REPLACE") {
		t.Fatalf("replace-all did not request a second confirmation: %q", out)
	}
	query("远端数据 2")
	libraryCLI(t, 0, "y\nREPLACE\n", "library", "refresh", name, "--replace-all")
	query("远端数据 3")

	snapshot.Store(librarySnapshot(t, remote, "结构已变更", "4", true))
	if out := libraryCLI(t, 1, "", "library", "refresh", name); !strings.Contains(out, "结构变更") {
		t.Fatalf("refresh did not report the schema mismatch: %q", out)
	}
	query("远端数据 3")
}

func TestLegacyLibraryQueryCLI(t *testing.T) {
	t.Setenv("BMCSHELL_HOME", t.TempDir())
	pkg := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "807d99b2-7d71-4c47-9908-3c40826410f0", Type: "library",
		Name: "旧版示例库", Summary: "查询旧版 JSON 记录",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json", Data: "records.json",
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, pkg))
	if out := libraryCLI(t, 0, "", "library", "query", pkg.Name, "DEMO01"); strings.TrimSpace(out) != "旧版设备" {
		t.Fatalf("legacy query = %q", out)
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
