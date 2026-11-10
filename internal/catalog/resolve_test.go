package catalog

import (
	"testing"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func TestResolveCatalogDependenciesAndSourcePin(t *testing.T) {
	const tool = "36ce47b5-5020-4646-85ef-31e311783b79"
	const book = "8a67f497-92a8-4be0-83a6-203ee44ec16c"
	entries := []Entry{
		{ID: tool, Name: "sbmc-tool-test", Type: "toolplugin", Version: "1.0.0", Source: "official"},
		{ID: tool, Name: "sbmc-tool-test", Type: "toolplugin", Version: "2.0.0", Source: "official", Dependencies: map[string]string{book: "1.0.0"}},
		{ID: tool, Name: "sbmc-tool-test", Type: "toolplugin", Version: "3.0.0", Source: "custom"},
		{ID: book, Name: "sbmc-book-test", Type: "book", Version: "1.0.0", Source: "official"},
		{ID: book, Name: "sbmc-book-test", Type: "book", Version: "2.0.0", Source: "official"},
	}
	if _, err := ResolveInstall(entries, nil, []string{"sbmc-tool-test"}, ""); err == nil {
		t.Fatal("multiple sources for the same ID should require a source")
	}
	assertVersions(t, mustResolve(t)(ResolveInstall(entries, nil, []string{"sbmc-tool-test"}, "official")),
		map[string]string{tool: "2.0.0", book: "1.0.0"})
	installed := []packages.Package{{Manifest: entryManifest(entries[0]), Source: "official"}}
	assertVersions(t, mustResolve(t)(ResolveUpgrade(entries, installed, []string{tool}, "")),
		map[string]string{tool: "2.0.0", book: "1.0.0"})
	entries = append(entries, Entry{ID: book, Name: "sbmc-book-test", Type: "book", Version: "1.0.0", Source: "custom"})
	assertVersions(t, mustResolve(t)(ResolveInstall(entries, nil, []string{tool}, "official")),
		map[string]string{tool: "2.0.0", book: "1.0.0"})
}

func TestResolveLibraryBacktracksToCompatibleToolAndLibrary(t *testing.T) {
	const library = "bbf259eb-4ca4-4e07-b760-bdbf7816b0af"
	const tool = "c44620b1-f307-41f9-bbf8-3305cc910cc5"
	const dependent = "d9d9e6fc-7138-48f5-9482-40ef6bb56c29"
	installed := []packages.Package{
		{Manifest: packages.Manifest{ID: library, Type: "library", Version: "1.0.0", Library: &librarydb.Contract{SchemaVersion: 1}}},
		{Manifest: packages.Manifest{ID: tool, Type: "toolplugin", Version: "1.0.0", Dependencies: map[string]string{library: "1.0.0"},
			LibrarySchemas: map[string]packages.LibrarySchemaRange{library: {Min: 1, Max: 1}}}},
		{Manifest: packages.Manifest{ID: dependent, Type: "toolplugin", Version: "1.0.0", Dependencies: map[string]string{tool: "1.0.0"}}},
	}
	entries := []Entry{
		{ID: library, Type: "library", Version: "2.1.0", Source: "official", LibrarySchemaVersion: 2, LibraryMigrations: map[string]string{"1": "migrate.sql"}},
		{ID: library, Type: "library", Version: "2.0.0", Source: "official", LibrarySchemaVersion: 2, LibraryMigrations: map[string]string{"1": "migrate.sql"}},
		{ID: tool, Type: "toolplugin", Version: "2.0.0", Source: "internal", Dependencies: map[string]string{library: "2.0.0"},
			LibrarySchemas: map[string]packages.LibrarySchemaRange{library: {Min: 2, Max: 2}}},
		{ID: dependent, Type: "toolplugin", Version: "2.0.0", Source: "internal", Dependencies: map[string]string{tool: "2.0.0"}},
	}
	assertVersions(t, mustResolve(t)(ResolveLibrary(entries, installed, library, 2, "official")),
		map[string]string{library: "2.0.0", tool: "2.0.0", dependent: "2.0.0"})
}

func mustResolve(t *testing.T) func([]Entry, error) []Entry {
	t.Helper()
	return func(entries []Entry, err error) []Entry {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
}

func assertVersions(t *testing.T, entries []Entry, versions map[string]string) {
	t.Helper()
	if len(entries) != len(versions) {
		t.Fatalf("unexpected plan: %#v", entries)
	}
	for _, entry := range entries {
		if versions[entry.ID] != entry.Version {
			t.Fatalf("unexpected plan entry: %#v", entry)
		}
	}
}
