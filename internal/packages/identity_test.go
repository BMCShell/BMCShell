package packages

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPackageIdentity(t *testing.T) {
	const name = "sbmc-tool-redfish-accounts"
	publisher, kind, shortName, err := ParseName(name)
	if err != nil || publisher != "sbmc" || kind != "toolplugin" || shortName != "redfish-accounts" {
		t.Fatalf("ParseName = %q, %q, %q, %v", publisher, kind, shortName, err)
	}
	if built, err := NameFor(publisher, kind, shortName); err != nil || built != name {
		t.Fatalf("NameFor = %q, %v", built, err)
	}
	for _, invalid := range []string{"SBMC-tool-redfish", "sbmc-toolplugin-redfish", "sbmc-tool-red_fish", "sbmc-tool-red--fish", "sbmc-tool-9fish", "sbmc-book-" + strings.Repeat("a", 49)} {
		if _, _, _, err := ParseName(invalid); err == nil {
			t.Errorf("accepted invalid name %q", invalid)
		}
	}
}

func TestLegacyPublisherDoesNotRequireCanonicalFields(t *testing.T) {
	legacy := Manifest{Name: "sample-tool-session", Publisher: "sample", Summary: "Legacy package using a publisher for its environment registry"}
	if err := legacy.validateIdentity(); err != nil {
		t.Fatalf("legacy package rejected: %v", err)
	}
}

func TestInstallCanonicalIdentity(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fixturePackage(t, bookID, "book", "sbmc-book-redfish")
	var m Manifest
	if err := json.Unmarshal(files[ManifestFile], &m); err != nil {
		t.Fatal(err)
	}
	m.Publisher, m.ShortName = "sbmc", "redfish"
	m.DisplayName, m.Description = "Redfish 工具书", "查询账户接口。"
	m.Summary = ""
	encode := func() {
		t.Helper()
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		files[ManifestFile] = data
	}
	m.Name = "other-book-redfish"
	encode()
	if _, err := s.Install(writeFixtureZIP(t, files)); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("mismatched identity: %v", err)
	}
	m.Name = "sbmc-book-redfish"
	encode()
	if _, err := s.Install(writeFixtureZIP(t, files)); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{m.Name, bookID} {
		p, err := s.Find(selector)
		if err != nil || p.Title() != m.DisplayName || p.Details() != m.Description {
			t.Fatalf("find %q = %v, %v", selector, p, err)
		}
	}
}

func TestAmbiguousLocalPackageName(t *testing.T) {
	list := []Package{
		{Manifest: Manifest{ID: bookID, Type: "book", Name: "Fixture", Version: "1.0.0"}},
		{Manifest: Manifest{ID: bundleID, Type: "bundle", Name: "Fixture", Version: "1.1.0"}},
	}
	var ambiguous *AmbiguousNameError
	if _, err := findPackage(list, "Fixture"); !errors.As(err, &ambiguous) || len(ambiguous.Matches) != 2 {
		t.Fatalf("find ambiguous package = %v", err)
	}
	for _, p := range list {
		if !strings.Contains(ambiguous.Error(), p.ID) {
			t.Errorf("missing candidate UUID %s", p.ID)
		}
	}
}
