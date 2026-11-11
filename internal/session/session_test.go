package session

import (
	"strings"
	"testing"

	"bmcshell/internal/packages"
	"bmcshell/internal/settings"
)

func TestSelectResolverByPackageName(t *testing.T) {
	pkg := packages.Package{Manifest: packages.Manifest{
		ID:        "f6511ae7-8bf1-4ac9-844b-71a035846606",
		Name:      "sbmc-tool-use-resolver",
		ShortName: "use-resolver",
		Version:   "1.0.0",
		Provides: packages.Provides{
			Resolvers: map[string]packages.Resolver{"sbmc": {Entry: "resolver.exe"}},
		},
	}}
	other := packages.Package{Manifest: packages.Manifest{
		ID:        "7761d975-f4da-459a-8181-2e1c3202c8b8",
		Name:      "test-tool-multi-resolver",
		ShortName: "multi-resolver",
		Provides: packages.Provides{
			Resolvers: map[string]packages.Resolver{pkg.Name: {}, "alternate": {}},
		},
	}}
	list := []packages.Package{other, pkg}
	fallback := &settings.Resolver{ExtensionID: pkg.ID, Export: "sbmc"}
	for _, name := range []string{pkg.Name, "sbmc", ""} {
		resolver, err := SelectResolver(list, name, fallback)
		if err != nil || resolver.Package.ID != pkg.ID || resolver.Name != "sbmc" || resolver.Selector() != pkg.Name {
			t.Fatalf("resolver %q = %+v, %v", name, resolver, err)
		}
	}
	resolver, err := SelectResolver(list, "alternate", nil)
	if err != nil || resolver.Package.ID != other.ID || resolver.Selector() != "alternate" {
		t.Fatalf("legacy export of a multi-resolver package = %+v, %v", resolver, err)
	}
	for name, want := range map[string]string{
		pkg.Name + "@" + pkg.Version: "省略版本",
		other.Name:                   "提供多个解析器",
	} {
		if _, err := SelectResolver(list, name, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("resolver %q returned %v, want %q", name, err, want)
		}
	}
	other.Name = pkg.Name
	if _, err := SelectResolver([]packages.Package{pkg, other}, pkg.Name, nil); err == nil {
		t.Fatal("accepted duplicate resolver package names")
	}
}

func TestSNMPEnterprisesPublicContext(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"", true}, {"0", true}, {"1.3.6.1.4.1.54357", true}, {"value", false}} {
		t.Setenv("BMCSHELL_CONTEXT", "{\"target.ip\":\"192.0.2.10\",\"snmp.enterprises\":\""+tc.value+"\"}")
		current, err := Current()
		if (err == nil) != tc.valid || tc.valid && current["snmp.enterprises"] != tc.value {
			t.Fatalf("context %q = %v, %v", tc.value, current, err)
		}
	}
}
