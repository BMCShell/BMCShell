package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"bmcshell/internal/packages"
)

func TestDoctorLoadsPluginRepairSuggestions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	p := catalogManifest("bda54a26-c941-47c6-8bd1-22e11776b300", "catalog-tool-fixture", "tool", "1.0.0")
	p.SystemDependencies = []packages.SystemDependency{{ID: "bmcshell-test-dependency-not-installed"}}
	p.RepairSuggestions = "repair.json"
	dir := catalogFixture(t, p)
	repairFile := filepath.Join(dir, p.RepairSuggestions)
	const good = `{"schema":1,"suggestions":[{"dependency":"bmcshell-test-dependency-not-installed","suggestion":"Install the test dependency."}]}`
	const bad = `{"schema":1,"suggestions":[{"dependency":"bmcshell-test-dependency-not-installed","suggestion":"test","command":"arbitrary shell text"}]}`
	if err := os.WriteFile(repairFile, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 1, "", "plugin", "install", "--file", dir)
	if err := os.WriteFile(repairFile, []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", dir)
	normal := libraryCLI(t, 0, "", "doctor")
	if !strings.Contains(normal, p.Name+"/"+p.SystemDependencies[0].ID+": missing") ||
		strings.Contains(normal, "Install the test dependency.") {
		t.Fatalf("unexpected compact diagnostic: %q", normal)
	}
	detailed := libraryCLI(t, 0, "", "doctor", "-d", "--filter", "missing")
	if !strings.Contains(detailed, "["+p.Name+"] Install the test dependency.") {
		t.Fatalf("missing plugin advice: %q", detailed)
	}
	if _, err := os.Stat(filepath.Join(home, "framework.db")); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 0, "", "plugin", "remove", p.Name)
	if output := libraryCLI(t, 0, "", "doctor", "-d"); strings.Contains(output, p.Name) {
		t.Fatalf("removed plugin remains in diagnostic: %q", output)
	}
}

func TestRepairActionsDeduplicateBothSources(t *testing.T) {
	records := []packages.RepairRecord{
		{SourceID: packages.RepairFrameworkSource, SourceName: "BMCShell", Dependency: "curl", Platform: "windows-amd64", Manager: "msys2", Package: "msys/curl"},
		{SourceID: "plugin", SourceName: "redfish", Dependency: "curl", Platform: "windows-amd64", Manager: "msys2", Package: "msys/curl"},
		{SourceID: "plugin", SourceName: "ssh", Dependency: "ssh", Platform: "windows-amd64", Manager: "msys2", Package: "msys/openssh"},
		{SourceID: "plugin", SourceName: "ipmi", Dependency: "ipmitool", Suggestion: "Manual guidance"},
	}
	actions := msys2RepairActions(records, map[string]bool{"curl": true, "ssh": true, "ipmitool": true})
	if len(actions) != 2 || len(actions["msys/curl"].dependencies) != 1 ||
		!actions["msys/curl"].sources["BMCShell"] || !actions["msys/curl"].sources["redfish"] ||
		!actions["msys/openssh"].sources["ssh"] {
		t.Fatalf("unexpected repair plan: %+v", actions)
	}
	names := sortedKeys(actions)
	if chosen, err := selectedRepairs(names, "2 1 2\n"); err != nil ||
		!slices.Equal(chosen, []string{"msys/openssh", "msys/curl"}) {
		t.Fatalf("unexpected selection: %v, %v", chosen, err)
	}
	if chosen, err := selectedRepairs(names, "1 invalid"); err == nil || len(chosen) != 0 {
		t.Fatalf("invalid input must not select any package: %v, %v", chosen, err)
	}
	if chosen, err := selectedRepairs(names, "\n"); err != nil || len(chosen) != 0 {
		t.Fatalf("blank input must cancel: %v, %v", chosen, err)
	}
}

func TestDamagedBookCanBeDiagnosedRepairedAndRemoved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	broken := catalogManifest("f3358650-f647-4ab2-9477-95f34a13706c", "catalog-book-fixture", "book", "1.0.0")
	broken.Data = "fixture.book.db"
	intact := catalogManifest("ab3f01fd-2cdb-470d-a702-004a890fc94b", "catalog-book-intact", "book", "1.0.0")
	intact.ShortName = "intact"
	intact.Data = "intact.book.db"
	dir := catalogFixture(t, broken)
	libraryCLI(t, 0, "", "plugin", "install", "--file", dir)
	libraryCLI(t, 0, "", "plugin", "install", "--file", catalogFixture(t, intact))
	// A legacy directory lock must not block mutation or diagnosis.
	if err := os.Mkdir(filepath.Join(home, ".plugin-mutation.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(home, "books", broken.ID, broken.Version, packages.ManifestFile)
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if out := libraryCLI(t, 0, "", "doctor", "-d"); !strings.Contains(out, home) ||
		!strings.Contains(out, "Damaged package: "+broken.Name) || !strings.Contains(out, broken.ID) {
		t.Fatalf("damaged package not diagnosed: %s", out)
	}
	if out := libraryCLI(t, 0, "", "plugin", "list"); !strings.Contains(out, intact.Name) || !strings.Contains(out, "damaged") {
		t.Fatalf("healthy and damaged packages not listed: %s", out)
	}
	if out := libraryCLI(t, 0, "", "book", "intact", "-v"); strings.TrimSpace(out) != intact.Version {
		t.Fatalf("unrelated Book unavailable: %s", out)
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", dir)
	if out := libraryCLI(t, 0, "", "book", "fixture", "-v"); strings.TrimSpace(out) != broken.Version {
		t.Fatalf("same-version repair did not restore Book: %s", out)
	}
	if err := os.WriteFile(manifest, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 0, "", "plugin", "remove", broken.ID)
	if out := libraryCLI(t, 0, "", "plugin", "list"); strings.Contains(out, broken.ID) ||
		!strings.Contains(out, intact.Name) {
		t.Fatalf("removal changed unrelated packages or kept damaged selection: %s", out)
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", dir)
	if out := libraryCLI(t, 0, "", "book", "fixture", "-v"); strings.TrimSpace(out) != broken.Version {
		t.Fatalf("reinstall after removal left Book unavailable: %s", out)
	}
}

func TestDamagedDependencyCanBeRepairedWithoutRemovingDependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	book := catalogManifest("1bc01268-1356-403f-be65-761147f6a404", "catalog-book-fixture", "book", "1.0.0")
	book.Data = "fixture.book.db"
	theme := catalogManifest("54ed615c-07e5-45cd-ab95-0fe1d0db81f1", "catalog-theme-fixture", "theme", "1.0.0")
	theme.Data = "theme.json"
	theme.Dependencies = map[string]string{book.ID: book.Version}
	dir := catalogFixture(t, book)
	libraryCLI(t, 0, "", "plugin", "install", "--file", dir)
	libraryCLI(t, 0, "", "plugin", "install", "--file", catalogFixture(t, theme))
	if err := os.Remove(filepath.Join(home, "books", book.ID, book.Version, packages.ManifestFile)); err != nil {
		t.Fatal(err)
	}
	store, err := packages.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := store.List(); err != nil || len(list) != 0 {
		t.Fatalf("dependent of damaged package remained usable: %v, %v", list, err)
	}
	libraryCLI(t, 1, "", "plugin", "remove", book.ID)
	libraryCLI(t, 0, "", "plugin", "install", "--file", dir)
	if list, err := store.List(); err != nil || len(list) != 2 {
		t.Fatalf("repair did not restore usable dependency set: %v, %v", list, err)
	}
}

func TestBookLegacyAliasesAndExplicitSelectors(t *testing.T) {
	t.Setenv("BMCSHELL_HOME", t.TempDir())
	oid := catalogManifest("4f39cb16-7e66-4fe2-9afe-fdfa56e7f005", "catalog-book-snmp-oid", "book", "1.0.0")
	oid.ShortName = "snmp-oid"
	oid.Data = "fixture.book.db"
	ipmi := catalogManifest("8822fff1-51d7-4e2f-a5e7-dec7b344f2cf", "catalog-book-oem-ipmi", "book", "1.0.1")
	ipmi.ShortName = "oem-ipmi"
	ipmi.Data = "fixture.book.db"
	libraryCLI(t, 0, "", "plugin", "install", "--file", catalogFixture(t, oid))
	libraryCLI(t, 0, "", "plugin", "install", "--file", catalogFixture(t, ipmi))
	for selector, version := range map[string]string{
		"oid": oid.Version, "snmp-oid": oid.Version, oid.Name: oid.Version, oid.ID: oid.Version,
		"oem_ipmi": ipmi.Version, "oem-ipmi": ipmi.Version, ipmi.Name: ipmi.Version, ipmi.ID: ipmi.Version,
	} {
		if out := libraryCLI(t, 0, "", "book", selector, "-v"); strings.TrimSpace(out) != version {
			t.Fatalf("selector %s resolved to %q; expected %s", selector, out, version)
		}
	}
}
