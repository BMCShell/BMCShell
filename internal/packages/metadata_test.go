package packages

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackageStateSurvivesLegacySelection(t *testing.T) {
	home := t.TempDir()
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range []string{"set-1", "set-2"} {
		if err := os.MkdirAll(store.setPath(set), 0700); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.openMetadata("rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE package_state (id INTEGER PRIMARY KEY, payload BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO package_state VALUES (1, ?)", []byte("{\"active\":\"set-1\"}")); err != nil {
		t.Fatal(err)
	}
	if set, err := store.readActiveSet(); err != nil || set.Active != "set-1" {
		t.Fatalf("legacy database selection = %+v, %v", set, err)
	}
	marker := filepath.Join(store.dir, ".active-set")
	if err := os.WriteFile(marker, []byte("{\"active\":\"set-2\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if set, err := store.readActiveSet(); err != nil || set.Active != "set-2" {
		t.Fatalf("legacy selection = %+v, %v", set, err)
	}
}
