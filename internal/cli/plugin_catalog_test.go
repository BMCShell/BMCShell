package cli

import (
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

func TestPluginCatalogCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	const toolID = "2a000000-0000-4000-8000-000000000001"
	const themeID = "2b000000-0000-4000-8000-000000000002"
	const libID = "2c000000-0000-4000-8000-000000000003"
	const secondThemeID = "2d000000-0000-4000-8000-000000000004"
	const toolName = "catalog-tool-fixture"
	const themeName = "catalog-theme-fixture"
	const libName = "catalog-library-fixture"
	contract := librarydb.ExampleContract()
	archives := make(map[string][]byte)
	var versions [][]catalog.Entry
	for _, version := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		tool := catalogManifest(toolID, toolName, "toolplugin", version)
		tool.Dependencies = map[string]string{themeID: version}
		theme := catalogManifest(themeID, themeName, "theme", version)
		theme.Data = "theme.json"
		library := catalogManifest(libID, libName, "library", version)
		library.Data, library.Library = "data.library.db", &contract
		var entries []catalog.Entry
		for _, p := range []packages.Manifest{tool, theme, library} {
			dir := catalogFixture(t, p)
			archive := archiveUpgradeFixture(t, dir)
			path := fmt.Sprintf("/%s-%s.zip", p.Type, version)
			archives[path] = archive
			entry := catalog.Entry{
				ID: p.ID, Name: p.Name, Type: p.Type, Version: p.Version, CoreAPI: p.CoreAPI,
				URL: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(archive)), Dependencies: p.Dependencies,
			}
			if p.Library != nil {
				entry.LibrarySchemaVersion = p.Library.SchemaVersion
			}
			if p.Type == "theme" && version == "3.0.0" {
				entry.SHA256 = strings.Repeat("0", 64)
			}
			entries = append(entries, entry)
		}
		versions = append(versions, entries)
	}
	theme := catalogManifest(secondThemeID, themeName, "theme", "1.0.0")
	theme.Data = "theme.json"
	archives["/second-theme.zip"] = archiveUpgradeFixture(t, catalogFixture(t, theme))
	versions[0] = append(versions[0], catalog.Entry{
		ID: secondThemeID, Name: themeName, Type: theme.Type, Version: theme.Version, CoreAPI: theme.CoreAPI,
		URL: "/second-theme.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(archives["/second-theme.zip"])),
	})
	var published atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/index.json" {
			_, _ = w.Write(published.Load().([]byte))
			return
		}
		if archive, found := archives[req.URL.Path]; found {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, req)
	}))
	defer server.Close()
	publish := func(entries []catalog.Entry) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"schema": 1, "packages": entries})
		if err != nil {
			t.Fatal(err)
		}
		published.Store(data)
		libraryCLI(t, 0, "", "plugin", "update")
	}
	libraryCLI(t, 0, "", "setting", "--set", "plugin.source.official", server.URL+"/index.json")
	libraryCLI(t, 0, "", "setting", "--set", "plugin.source.team", server.URL+"/index.json?key=private-value")
	if list := libraryCLI(t, 0, "", "setting"); !strings.Contains(list, "plugin.source.official") ||
		!strings.Contains(list, "plugin.source.team") || strings.Contains(list, "private-value") {
		t.Fatalf("source list = %q", list)
	}
	libraryCLI(t, 0, "", "setting", "--reset", "plugin.source.team")
	publish(append(append([]catalog.Entry{}, versions[0]...), versions[1]...))
	if output := libraryCLI(t, 0, "", "plugin", "search", toolName); !strings.Contains(output, toolName+"@2.0.0") || strings.Contains(output, toolName+"@1.0.0") {
		t.Fatalf("plugin search = %q", output)
	}
	libraryCLI(t, 0, "", "plugin", "install", toolName+"@1.0.0")
	libraryCLI(t, 0, "", "plugin", "install", libName+"@1.0.0")
	assertCatalogPackageVersions(t, "1.0.0", toolID, themeID, libID)
	libraryCLI(t, 1, "", "plugin", "install", themeName)
	libraryCLI(t, 1, "", "plugin", "install", toolID)

	publish(versions[1])
	libraryCLI(t, 0, "", "plugin", "upgrade", toolName)
	assertCatalogPackageVersions(t, "2.0.0", toolID, themeID)
	assertCatalogPackageVersions(t, "1.0.0", libID)
	libraryCLI(t, 0, "", "plugin", "upgrade", libName)
	assertCatalogPackageVersions(t, "2.0.0", libID)
	if got := libraryCLI(t, 0, "", "library", "fixture"); !strings.Contains(got, "结构版本") {
		t.Fatalf("Library info = %q", got)
	}

	publish(versions[2])
	libraryCLI(t, 1, "", "plugin", "upgrade", toolName)
	assertCatalogPackageVersions(t, "2.0.0", toolID, themeID, libID)
}

func catalogManifest(id, name, kind, version string) packages.Manifest {
	return packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: id, Name: name, Type: kind, Version: version, Publisher: "catalog",
		ShortName: "fixture", DisplayName: name, Help: "help.json", Changelog: "changelog.json",
	}
}

func catalogFixture(t *testing.T, manifest packages.Manifest) string {
	t.Helper()
	if manifest.Library != nil {
		return libraryFixture(t, manifest)
	}
	root := t.TempDir()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{packages.ManifestFile: data, manifest.Help: []byte("{}"), manifest.Changelog: []byte("{}")}
	if manifest.Data != "" {
		files[manifest.Data] = []byte(`{"prefix":"[fixture] "}`)
	}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(root, path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func assertCatalogPackageVersions(t *testing.T, version string, ids ...string) {
	t.Helper()
	store, err := packages.Open(os.Getenv("BMCSHELL_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		pkg, err := store.Find(id)
		if err != nil || pkg.Version != version || pkg.Source != "official" {
			t.Fatalf("package %s version/source = %s/%s, err %v; want official version %s", id, pkg.Version, pkg.Source, err, version)
		}
	}
}
