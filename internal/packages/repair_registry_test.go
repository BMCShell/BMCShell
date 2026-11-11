package packages

import (
	"errors"
	"testing"

	"bmcshell/internal/filelock"
)

func TestSyncRepairSuggestionsKeepsCurrentPackageSnapshot(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := writeFixtureZIP(t, fixturePackage(t, bookID, "tool", "Fixture Tool"))
	plugin, err := store.Install(fixture)
	if err != nil {
		t.Fatal(err)
	}
	framework := RepairRecord{
		SourceID: RepairFrameworkSource, Dependency: "curl", Suggestion: "Install curl",
		Platform: "windows", Manager: "pacman", Package: "curl",
	}
	pluginRecord := framework
	pluginRecord.SourceID = plugin.ID

	installed, _, indexed, err := store.SyncRepairSuggestions(func(installed []Package) ([]RepairRecord, error) {
		if len(installed) != 1 || installed[0].ID != plugin.ID {
			t.Fatalf("collector got installed packages %+v", installed)
		}
		if err := store.Remove(plugin.ID); !errors.Is(err, filelock.ErrBusy) {
			t.Fatalf("package mutation during collection = %v", err)
		}
		return []RepairRecord{pluginRecord, framework, pluginRecord}, nil
	})
	if err != nil || len(installed) != 1 || len(indexed) != 2 {
		t.Fatalf("first repair index = %+v, packages = %+v, error = %v", indexed, installed, err)
	}
	if indexed[0].SourceID != RepairFrameworkSource || indexed[0].SourceName != "BMCShell" ||
		indexed[1].SourceID != plugin.ID || indexed[1].SourceName != plugin.Name {
		t.Fatalf("repair sources = %+v", indexed)
	}

	if err := store.Remove(plugin.ID); err != nil {
		t.Fatal(err)
	}
	installed, _, indexed, err = store.SyncRepairSuggestions(func(installed []Package) ([]RepairRecord, error) {
		return []RepairRecord{framework}, nil
	})
	if err != nil || len(installed) != 0 || len(indexed) != 1 || indexed[0].SourceID != RepairFrameworkSource {
		t.Fatalf("refreshed repair index = %+v, packages = %+v, error = %v", indexed, installed, err)
	}

	if _, _, _, err := store.SyncRepairSuggestions(func([]Package) ([]RepairRecord, error) {
		return []RepairRecord{pluginRecord}, nil
	}); err == nil {
		t.Fatal("accepted a repair suggestion from an uninstalled package")
	}
	db, err := store.openMetadata("ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM repair_suggestions WHERE source_id = ?", RepairFrameworkSource).Scan(&count); err != nil || count != 1 {
		t.Fatalf("previous repair index was not preserved after failure: count = %d, error = %v", count, err)
	}
}
