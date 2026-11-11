package packages

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyPackageLayoutMigratesOnCommit(t *testing.T) {
	for _, legacySet := range []string{"", "set-123"} {
		t.Run("legacy-"+legacySet, func(t *testing.T) {
			root := t.TempDir()
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			legacyBook := filepath.Join(store.setPath(legacySet), bookID)
			if err := os.MkdirAll(legacyBook, 0700); err != nil {
				t.Fatal(err)
			}
			fixture := writeFixtureZIP(t, fixturePackage(t, bookID, "book", "Fixture Book"))
			if _, err := store.stagePackage(fixture, legacyBook); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(legacyBook, originFile), []byte("official\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if legacySet != "" {
				marker := filepath.Join(store.dir, ".active-set")
				if err := os.WriteFile(marker, []byte("{\"active\":\"set-123\"}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			installed, err := store.List()
			if err != nil || len(installed) != 1 || installed[0].Root != legacyBook {
				t.Fatalf("legacy packages = %+v, %v", installed, err)
			}
			if _, err := os.Stat(filepath.Join(root, "framework.db")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("reading legacy layout changed metadata: %v", err)
			}

			tool := writeFixtureZIP(t, fixturePackage(t, bundleID, "toolplugin", "Fixture Tool"))
			plan, err := store.PrepareChanges([]string{tool}, installed)
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			if p, err := findPackage(plan.Packages(), bookID); err != nil || p.Root != legacyBook {
				t.Fatalf("planning copied unchanged payload: %+v, %v", p, err)
			}
			aborted := errors.New("before commit failed")
			if err := store.CommitChanges(plan, func() error { return aborted }); !errors.Is(err, aborted) {
				t.Fatalf("interrupted commit returned %v", err)
			}
			if p, err := store.Find(bookID); err != nil || p.Root != legacyBook {
				t.Fatalf("interrupted commit changed active path: %+v, %v", p, err)
			}
			if err := store.CommitChanges(plan, nil); err != nil {
				t.Fatal(err)
			}
			bookPath := filepath.Join(root, "books", bookID, "1.0.0")
			if p, err := store.Find(bookID); err != nil || p.Root != bookPath || p.Source != "official" {
				t.Fatalf("migrated Book = %+v, %v", p, err)
			}
			if p, err := store.Find(bundleID); err != nil || p.Root != filepath.Join(root, "plugins", "tool", bundleID, "1.0.0") {
				t.Fatalf("registered Tool = %+v, %v", p, err)
			}
			if _, err := os.Stat(filepath.Join(legacyBook, ManifestFile)); err != nil {
				t.Fatalf("legacy payload is gone: %v", err)
			}
			if err := store.Remove(bookID); err != nil {
				t.Fatal(err)
			}
			if err := store.Remove(bundleID); err != nil {
				t.Fatal(err)
			}
			if list, err := store.List(); err != nil || len(list) != 0 {
				t.Fatalf("removed packages reappeared from legacy layout: %+v, %v", list, err)
			}
		})
	}
}

func TestPackageAndLibraryGenerationActivateTogether(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	oldLibrary, _ := upgradeFixtures(t, "1.0.0", 1)
	newLibrary, _ := upgradeFixtures(t, "2.0.0", 1)
	p, err := store.Install(oldLibrary)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "libraries", upgradeLibraryID, "releases", "1.0.0")
	if p.Root != want {
		t.Fatalf("Library release path = %s, want %s", p.Root, want)
	}
	if _, err := os.Stat(store.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh installation created a legacy package directory: %v", err)
	}
	for _, generation := range []string{"gen-old", "gen-new"} {
		if err := os.MkdirAll(filepath.Join(root, "libraries", p.ID, generation), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetLibraryGeneration(p.ID, p.Version, "", "gen-old"); err != nil {
		t.Fatal(err)
	}
	installed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.PrepareChanges([]string{newLibrary}, installed)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	activation := LibraryActivation{ID: p.ID, Version: "2.0.0", Previous: "gen-stale", Generation: "gen-new"}
	if err := store.CommitChangesWithLibraries(plan, []LibraryActivation{activation}, nil); err == nil {
		t.Fatal("accepted a stale generation selection")
	}
	if current, err := store.Find(p.ID); err != nil || current.Version != p.Version {
		t.Fatalf("failed generation selection changed package: %+v, %v", current, err)
	}
	if generation, err := store.LibraryGeneration(p.ID, "2.0.0"); err != nil || generation != "" {
		t.Fatalf("failed selection registered generation %q, %v", generation, err)
	}

	activation.Previous = ""
	if err := store.CommitChangesWithLibraries(plan, []LibraryActivation{activation}, nil); err != nil {
		t.Fatal(err)
	}
	if current, err := store.Find(p.ID); err != nil || current.Version != "2.0.0" {
		t.Fatalf("active Library = %+v, %v", current, err)
	}
	if generation, err := store.LibraryGeneration(p.ID, "2.0.0"); err != nil || generation != "gen-new" {
		t.Fatalf("active generation = %q, %v", generation, err)
	}
	if generation, err := store.LibraryGeneration(p.ID, "1.0.0"); err != nil || generation != "gen-old" {
		t.Fatalf("retained generation = %q, %v", generation, err)
	}
	if err := store.SetLibraryGeneration(p.ID, "1.0.0", "gen-old", "gen-new"); err == nil {
		t.Fatal("edited a generation belonging to a previous package version")
	}
}
