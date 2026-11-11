package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func TestResolverEnvironmentUsesDeclaredWorkingLibrary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	t.Setenv(resolverLibrariesEnv, `{"unexpected":"untrusted.db"}`)
	t.Setenv(resolverBashEnv, "untrusted-bash")
	t.Setenv(strings.ToLower(resolverCoreExeEnv), "untrusted-core")
	bash := filepath.Join(home, "bash")
	t.Setenv("BMCSHELL_BRIDGE_BASH", bash)
	t.Setenv("BMCSHELL_BRIDGE_INTERACTIVE", "0")
	contract := librarydb.ExampleContract()
	manifest := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "93ca11e1-1c9a-4dd9-8f3e-cf6fa6353851", Type: "library",
		Name: "sbmc-library-resolver-fixture", Publisher: "sbmc", ShortName: "resolver-fixture", DisplayName: "Resolver fixture",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json", Data: "fixture.library.db",
		Library: &contract,
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, manifest))
	a, state := installedLibrary(t, home, manifest.ID)
	installed, err := a.store.List()
	if err != nil {
		t.Fatal(err)
	}
	core, err := os.Executable()
	if err != nil || !filepath.IsAbs(core) {
		t.Fatalf("cannot resolve test executable path %q: %v", core, err)
	}
	read := func(resolver packages.Package) (map[string]string, string) {
		t.Helper()
		environment, err := a.resolverEnvironment(resolver, installed, nil)
		if err != nil {
			t.Fatal(err)
		}
		var libraries map[string]string
		var resolverBash string
		seen := map[string]bool{}
		for _, entry := range environment {
			name, value, _ := strings.Cut(entry, "=")
			canonicalName := strings.ToUpper(name)
			if canonicalName != resolverLibrariesEnv && canonicalName != resolverBashEnv && canonicalName != resolverCoreExeEnv && !strings.HasPrefix(canonicalName, "BMCSHELL_BRIDGE_") {
				continue
			}
			if name != canonicalName || seen[name] || strings.HasPrefix(name, "BMCSHELL_BRIDGE_") {
				t.Fatalf("unexpected resolver environment variable %s", name)
			}
			seen[name] = true
			switch name {
			case resolverLibrariesEnv:
				if err := json.Unmarshal([]byte(value), &libraries); err != nil {
					t.Fatal(err)
				}
			case resolverBashEnv:
				resolverBash = value
			case resolverCoreExeEnv:
				if value != core {
					t.Fatalf("resolver core executable = %q, want %q", value, core)
				}
			}
		}
		if !seen[resolverLibrariesEnv] || !seen[resolverBashEnv] || !seen[resolverCoreExeEnv] {
			t.Fatal("resolver environment was not set")
		}
		return libraries, resolverBash
	}
	if libraries, path := read(packages.Package{}); len(libraries) != 0 || path != "" {
		t.Fatalf("non-interactive resolver environment = %v, %q", libraries, path)
	}

	t.Setenv("BMCSHELL_BRIDGE_INTERACTIVE", "1")
	resolver := packages.Package{Manifest: packages.Manifest{
		Name: "sbmc-tool-resolver-fixture",
		LibrarySchemas: map[string]packages.LibrarySchemaRange{
			manifest.ID: {Min: contract.SchemaVersion, Max: contract.SchemaVersion},
		},
	}}
	libraries, path := read(resolver)
	if len(libraries) != 1 || libraries[manifest.ID] != state.work || state.work == state.baseline || path != bash {
		t.Fatalf("interactive resolver environment = %v, %q, want working file %q and Bash %q", libraries, path, state.work, bash)
	}
}
