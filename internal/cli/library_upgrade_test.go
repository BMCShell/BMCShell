package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"bmcshell/internal/catalog"
	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func TestLibraryJointUpgradeAndRollback(t *testing.T) {
	t.Run("local packages", func(t *testing.T) { testLibraryJointUpgrade(t, false) })
	t.Run("software source", func(t *testing.T) { testLibraryJointUpgrade(t, true) })
}

func testLibraryJointUpgrade(t *testing.T, useCatalog bool) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	contract := librarydb.ExampleContract()
	library := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "a32836ef-9ce5-43d3-a854-7a717aaec906", Type: "library",
		Name: "sbmc-library-upgrade-fixture", Publisher: "sbmc", ShortName: "upgrade-fixture",
		DisplayName: "联合升级库", Version: "1.0.0", Help: "help.json", Changelog: "changelog.json",
		Data: "data.library.db", Library: &contract,
	}
	tool := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "31211564-a66d-4090-aa86-34e72a906870", Type: "toolplugin",
		Name: "sbmc-tool-upgrade-fixture", Publisher: "sbmc", ShortName: "upgrade-fixture",
		DisplayName: "联合升级工具", Version: "1.0.0", Help: "help.json", Changelog: "changelog.json",
		Dependencies:   map[string]string{library.ID: "1.0.0"},
		LibrarySchemas: map[string]packages.LibrarySchemaRange{library.ID: {Min: 1, Max: 1}},
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, library))
	libraryCLI(t, 0, "", "plugin", "install", "--file", upgradeToolFixture(t, tool))

	remote := filepath.Join(t.TempDir(), "remote.library.db")
	if err := librarydb.CreateExample(remote, library.ID); err != nil {
		t.Fatal(err)
	}
	var contents atomic.Value
	contents.Store(librarySnapshot(t, remote, "旧版远端记录", "2", false))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(contents.Load().([]byte))
	}))
	defer server.Close()
	libraryCLI(t, 0, "", "library", "source", library.ID, server.URL)
	libraryCLI(t, 0, "y\n", "library", "refresh", library.ID)
	oldRoot := filepath.Join(home, "libraries", library.ID, library.Version)
	oldGeneration, err := readLibraryGeneration(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	oldWork := filepath.Join(oldRoot, oldGeneration, "work.library.db")
	originalWork, err := os.ReadFile(oldWork)
	if err != nil {
		t.Fatal(err)
	}

	contractV2 := contract
	contractV2.SchemaVersion = 2
	library.Version, library.Library = "2.0.0", &contractV2
	library.LibraryMigrations = map[string]string{"1": "migrate.sql"}
	libraryV2 := libraryFixture(t, library)
	if err := os.WriteFile(filepath.Join(libraryV2, "migrate.sql"), []byte("ALTER TABLE records ADD COLUMN note TEXT;"), 0600); err != nil {
		t.Fatal(err)
	}
	librarySnapshot(t, filepath.Join(libraryV2, library.Data), "新版模板", "1", true)
	tool.Version = "2.0.0"
	tool.Dependencies = map[string]string{library.ID: "2.0.0"}
	tool.LibrarySchemas = map[string]packages.LibrarySchemaRange{library.ID: {Min: 2, Max: 2}}
	toolV2 := upgradeToolFixture(t, tool)
	contents.Store(librarySnapshot(t, remote, "新版远端记录", "3", true))

	check := func(version, value string) {
		t.Helper()
		for _, id := range []string{tool.ID, library.ID} {
			info := libraryCLI(t, 0, "", "plugin", "show", id)
			if !strings.Contains(info, "Version: "+version) {
				t.Fatalf("wrong version for %s: %s", id, info)
			}
		}
		got := libraryCLI(t, 0, "", "library", "query", library.ID, "DEMO01")
		if strings.TrimSpace(got) != value {
			t.Fatalf("query = %q, want %q", got, value)
		}
	}
	args := []string{"library", "refresh", library.ID}
	var publishComplete func()
	if useCatalog {
		publishComplete = serveLibraryUpgradeCatalog(t, home, library, libraryV2, tool, toolV2)
	} else {
		args = append(args, "--upgrade-with", libraryV2)
	}
	libraryCLI(t, 1, "", args...)
	check("1.0.0", "旧版远端记录")
	if useCatalog {
		publishComplete()
	} else {
		args = append(args, "--upgrade-with", toolV2)
	}
	previewArgs := append(args, "--dry-run")
	if useCatalog {
		previewArgs = append(previewArgs, "--source", "team")
	}
	preview := libraryCLI(t, 0, "", previewArgs...)
	if useCatalog && (!strings.Contains(preview, "源 team") || !strings.Contains(preview, "1.0.0 → 2.0.0")) {
		t.Fatalf("software-source plan did not show the source and versions: %s", preview)
	}
	check("1.0.0", "旧版远端记录")
	libraryCLI(t, 0, "n\n", args...)
	check("1.0.0", "旧版远端记录")
	libraryCLI(t, 0, "y\n", args...)
	check("2.0.0", "新版远端记录")
	if useCatalog {
		checkLibraryUpgradeSources(t, home, "team", library.ID, tool.ID)
	}
	if err := os.WriteFile(oldWork, append(originalWork, 0), 0600); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 1, "y\n", "library", "rollback", library.ID)
	check("2.0.0", "新版远端记录")
	if err := os.WriteFile(oldWork, originalWork, 0600); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 0, "y\n", "library", "rollback", library.ID)
	check("1.0.0", "旧版远端记录")
	if useCatalog {
		checkLibraryUpgradeSources(t, home, "", library.ID, tool.ID)
	}
}

func checkLibraryUpgradeSources(t *testing.T, home, source string, ids ...string) {
	t.Helper()
	store, err := packages.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		p, err := store.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Source != source {
			t.Fatalf("package %s source = %q, want %q", id, p.Source, source)
		}
	}
}

func serveLibraryUpgradeCatalog(t *testing.T, home string, library packages.Manifest, libraryDir string, tool packages.Manifest, toolDir string) func() {
	t.Helper()
	libraryZIP := archiveUpgradeFixture(t, libraryDir)
	toolZIP := archiveUpgradeFixture(t, toolDir)
	entries := []catalog.Entry{
		{
			ID: library.ID, Name: library.Name, Type: library.Type, Version: library.Version,
			CoreAPI: library.CoreAPI, URL: "library.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(libraryZIP)),
			LibrarySchemaVersion: library.Library.SchemaVersion, LibraryMigrations: library.LibraryMigrations,
		},
		{
			ID: tool.ID, Name: tool.Name, Type: tool.Type, Version: tool.Version,
			CoreAPI: tool.CoreAPI, URL: "tool.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(toolZIP)),
			Dependencies: tool.Dependencies, LibrarySchemas: tool.LibrarySchemas,
		},
	}
	var index atomic.Value
	var handler http.ServeMux
	handler.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(index.Load().([]byte))
	})
	handler.HandleFunc("/library.zip", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(libraryZIP) })
	handler.HandleFunc("/tool.zip", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(toolZIP) })
	server := httptest.NewServer(&handler)
	t.Cleanup(server.Close)
	sources := catalog.Open(home)
	if err := sources.SetSource(catalog.Source{Name: "team", URL: server.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	publish := func(entries []catalog.Entry) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"schema": 1, "packages": entries})
		if err != nil {
			t.Fatal(err)
		}
		index.Store(data)
		if _, err := sources.Refresh(context.Background(), "team"); err != nil {
			t.Fatal(err)
		}
	}
	publish(entries[:1])
	return func() { publish(entries) }
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
