package packages

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPayloadRetentionKeepsVerifiedPredecessorAndUntrackedHistory(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	release := func(version string) string {
		return retentionFixture(t, bookID, "book", "Retention Book", version, nil)
	}
	bookPath := func(version string) string {
		return filepath.Join(root, "books", bookID, version)
	}

	// An installation from before retention tracking might contain this release.
	unknown := bookPath("0.9.0")
	if err := os.MkdirAll(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.stagePackage(release("0.9.0"), unknown); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(release("1.0.0")); err != nil {
		t.Fatal(err)
	}
	assertPayloadPlan(t, store)

	applyRetentionFixtures(t, store, release("2.0.0"))
	assertPayloadPlan(t, store)
	applyRetentionFixtures(t, store, release("3.0.0"))
	assertPayloadPlan(t, store, bookPath("1.0.0"))

	if err := os.Remove(filepath.Join(bookPath("2.0.0"), ManifestFile)); err != nil {
		t.Fatal(err)
	}
	// A damaged 2.0.0 cannot serve as the rollback version; retain 1.0.0.
	assertPayloadPlan(t, store)
	if _, err := os.Stat(filepath.Join(unknown, ManifestFile)); err != nil {
		t.Fatalf("planning touched the older release: %v", err)
	}
}

func TestPayloadRetentionProtectsExactDependenciesAndRollback(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	book := func(version string) string {
		return retentionFixture(t, bookID, "book", "Retention Book", version, nil)
	}
	tool := func(version string, dependencies map[string]string) string {
		return retentionFixture(t, upgradeToolID, "toolplugin", "Retention Tool", version, dependencies)
	}
	for _, file := range []string{book("1.0.0"), tool("1.0.0", map[string]string{bookID: "1.0.0"})} {
		if _, err := store.Install(file); err != nil {
			t.Fatal(err)
		}
	}
	applyRetentionFixtures(t, store, book("2.0.0"), tool("2.0.0", nil))
	applyRetentionFixtures(t, store, book("3.0.0"))
	assertPayloadPlan(t, store)

	applyRetentionFixtures(t, store, tool("3.0.0", nil))
	oldBook := filepath.Join(root, "books", bookID, "1.0.0")
	oldTool := filepath.Join(root, "plugins", "tool", upgradeToolID, "1.0.0")
	assertPayloadPlan(t, store, oldBook, oldTool)
	entries, _, err := store.registeredPackages()
	if err != nil {
		t.Fatal(err)
	}
	rollback := &registryRollback{
		Previous: []registryEntry{{ID: upgradeToolID, Kind: "tool", Version: "1.0.0"}},
	}
	if err := store.writeRegistry(entries, nil, rollback, false); err != nil {
		t.Fatal(err)
	}
	assertPayloadPlan(t, store)
}

func TestPayloadRetentionFailedActivationDoesNotRecordSupersededRelease(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := func(version string) string {
		return retentionFixture(t, bookID, "book", "Retention Book", version, nil)
	}
	if _, err := store.Install(fixture("1.0.0")); err != nil {
		t.Fatal(err)
	}

	// Fail the metadata transaction after history recording, using an
	// activation whose generation selection cannot be committed.
	entries, _, err := store.registeredPackages()
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Version = "2.0.0"

	gen := filepath.Join(filepath.Dir(store.dir), "libraries", upgradeLibraryID, "gen-next")
	if err := os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	bad := LibraryActivation{ID: upgradeLibraryID, Version: "1.0.0", Previous: "gen-missing", Generation: "gen-next"}
	if err := store.writeRegistry(entries, []LibraryActivation{bad}, nil, false); err == nil {
		t.Fatal("registry unexpectedly accepted a stale generation selection")
	}
	history, _, err := store.supersededPayloads()
	if err != nil || len(history) != 0 {
		t.Fatalf("aborted write retained history = %v, %v", history, err)
	}
}

func retentionFixture(t *testing.T, id, kind, name, version string, dependencies map[string]string) string {
	t.Helper()
	files := fixturePackage(t, id, kind, name)
	editFixtureManifest(t, files, func(m *Manifest) {
		m.Version = version
		m.Dependencies = dependencies
	})
	return writeFixtureZIP(t, files)
}

func applyRetentionFixtures(t *testing.T, store *Store, fixtures ...string) {
	t.Helper()
	installed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.PrepareChanges(fixtures, installed)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	if err := store.CommitChanges(plan, nil); err != nil {
		t.Fatal(err)
	}
}

func assertPayloadPlan(t *testing.T, store *Store, expected ...string) {
	t.Helper()
	actual, err := store.PlanPayloadRetention()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		t.Fatalf("retention plan = %v, want %v", actual, expected)
	}
	for _, path := range actual {
		if _, err := os.Stat(filepath.Join(path, ManifestFile)); errors.Is(err, os.ErrNotExist) {
			t.Fatalf("planner already removed %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
