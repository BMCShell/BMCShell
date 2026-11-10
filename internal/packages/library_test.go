package packages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/librarydb"
)

func TestInstallSQLiteLibrary(t *testing.T) {
	const id = "c5709ad6-6d2c-472e-9ec6-97804cb62d16"
	const dataPath = "library/records.library.db"
	source := filepath.Join(t.TempDir(), "records.library.db")
	if err := librarydb.CreateExample(source, id); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	contract := librarydb.ExampleContract()
	for _, tc := range []struct {
		name     string
		id       string
		path     string
		contract *librarydb.Contract
		wantErr  string
	}{
		{"valid", id, dataPath, &contract, ""},
		{"missing contract", id, dataPath, nil, "declare its library contract"},
		{"wrong extension", id, "library/records.db", &contract, "must end in .library.db"},
		{"wrong UUID", bookID, dataPath, &contract, "invalid Library data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			files := fixturePackage(t, tc.id, "library", "Fixture Library")
			var m Manifest
			if err := json.Unmarshal(files[ManifestFile], &m); err != nil {
				t.Fatal(err)
			}
			m.Data, m.Library = tc.path, tc.contract
			files[tc.path] = data
			files[ManifestFile], err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Install(writeFixtureZIP(t, files))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("install error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := store.Find(m.Name)
			if err != nil || p.Library == nil || p.Library.SchemaVersion != contract.SchemaVersion {
				t.Fatalf("installed library = %+v, error = %v", p.Library, err)
			}
		})
	}
}
