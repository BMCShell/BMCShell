package packages

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/librarydb"
)

const (
	upgradeLibraryID = "37bc4c87-4a7d-44f9-9fac-7198d7febc45"
	upgradeToolID    = "51892a57-1c71-4fd0-bdf0-a81e96ecf653"
)

func TestLibraryPackageSetUpgrade(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldLibrary, oldTool := upgradeFixtures(t, "1.0.0", 1)
	newLibrary, newTool := upgradeFixtures(t, "2.0.0", 2)
	for _, file := range []string{oldLibrary, oldTool} {
		if _, err := s.Install(file); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PrepareUpgrade([]string{newLibrary}); err == nil || !strings.Contains(err.Error(), "结构版本为 1 到 1，实际为 2") {
		t.Fatalf("upgrade without compatible tool = %v", err)
	}
	plan, err := s.PrepareUpgrade([]string{newLibrary, newTool})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	if len(plan.Packages()) != 2 || len(plan.OriginalPackages()) != 2 {
		t.Fatalf("unexpected plan: %v -> %v", plan.OriginalPackages(), plan.Packages())
	}
	assertVersions := func(version string) {
		t.Helper()
		for _, id := range []string{upgradeLibraryID, upgradeToolID} {
			p, err := s.Find(id)
			if err != nil || p.Version != version {
				t.Fatalf("package %s = %s, %v; want %s", id, p.Version, err, version)
			}
		}
	}
	assertVersions("1.0.0")
	callbackError := errors.New("test data commit failure")
	const checkpoint = "previous-library-data"
	if err := s.CommitUpgrade(plan, upgradeLibraryID, checkpoint, func() error { return callbackError }); !errors.Is(err, callbackError) {
		t.Fatalf("failed data commit = %v", err)
	}
	assertVersions("1.0.0")
	other := writeFixtureZIP(t, fixturePackage(t, bundleID, "bundle", "Unrelated Bundle"))
	if _, err := s.Install(other); err != nil {
		t.Fatal(err)
	}
	called := false
	callback := func() error { called = true; return nil }
	if err := s.CommitUpgrade(plan, upgradeLibraryID, checkpoint, callback); err == nil || called {
		t.Fatalf("stale plan committed or ran data callback: %v, %v", err, called)
	}
	if err := s.Remove(bundleID); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitUpgrade(plan, upgradeLibraryID, checkpoint, callback); err != nil || !called {
		t.Fatalf("package set did not commit: %v, %v", err, called)
	}
	assertVersions("2.0.0")
	if err := s.RollbackUpgrade(upgradeToolID, nil); err == nil {
		t.Fatal("rollback accepted an unrelated package ID")
	}
	installed, err := s.Install(other)
	if err != nil || filepath.Dir(installed.Root) == s.dir {
		t.Fatalf("new install did not target active package set: %v, %v", installed.Root, err)
	}
	if err := s.RollbackUpgrade(upgradeLibraryID, nil); err == nil {
		t.Fatal("rollback would discard unrelated package mutation")
	}
	if err := s.Remove(bundleID); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackUpgrade(upgradeLibraryID, func(previous Package, actualCheckpoint string) error {
		if previous.ID != upgradeLibraryID || previous.Version != "1.0.0" || actualCheckpoint != checkpoint {
			t.Fatalf("rollback checkpoint = %s %s %q", previous.ID, previous.Version, actualCheckpoint)
		}
		return callbackError
	}); !errors.Is(err, callbackError) {
		t.Fatalf("rollback accepted invalid checkpoint: %v", err)
	}
	assertVersions("2.0.0")
	if err := s.RollbackUpgrade(upgradeLibraryID, nil); err != nil {
		t.Fatal(err)
	}
	assertVersions("1.0.0")
}

func upgradeFixtures(t *testing.T, version string, schema int) (library, tool string) {
	t.Helper()
	const dbPath = "records.library.db"
	path := filepath.Join(t.TempDir(), dbPath)
	if err := librarydb.CreateExample(path, upgradeLibraryID); err != nil {
		t.Fatal(err)
	}
	if schema == 2 {
		db, err := librarydb.OpenReadWrite(path)
		if err != nil {
			t.Fatal(err)
		}
		_, changeErr := db.Exec("UPDATE _bmcshell_library SET schema_version = 2; PRAGMA user_version = 2")
		if err := errors.Join(changeErr, db.Close()); err != nil {
			t.Fatal(err)
		}
	}
	libraryFiles := fixturePackage(t, upgradeLibraryID, "library", "Upgradeable Library")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	libraryFiles[dbPath] = payload
	editFixtureManifest(t, libraryFiles, func(m *Manifest) {
		contract := librarydb.ExampleContract()
		contract.SchemaVersion = schema
		m.Version, m.Data, m.Library = version, dbPath, &contract
	})
	toolFiles := fixturePackage(t, upgradeToolID, "toolplugin", "Upgradeable Tool")
	editFixtureManifest(t, toolFiles, func(m *Manifest) {
		m.Version = version
		m.LibrarySchemas = map[string]LibrarySchemaRange{upgradeLibraryID: {Min: schema, Max: schema}}
	})
	return writeFixtureZIP(t, libraryFiles), writeFixtureZIP(t, toolFiles)
}

func editFixtureManifest(t *testing.T, files map[string][]byte, edit func(*Manifest)) {
	t.Helper()
	var m Manifest
	if err := json.Unmarshal(files[ManifestFile], &m); err != nil {
		t.Fatal(err)
	}
	edit(&m)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	files[ManifestFile] = data
}
