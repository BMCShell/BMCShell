package packages

import (
	"strings"
	"testing"
)

func TestResolverStatusFieldsValidation(t *testing.T) {
	model := StatusField{Key: "target.model", Label: "机型"}
	for _, tc := range []struct {
		name   string
		fields []StatusField
	}{
		{"duplicate", []StatusField{model, model}},
		{"wrong namespace", []StatusField{{Key: "oem.other.cid", Label: "CID"}}},
		{"private", []StatusField{{Key: "auth.password", Label: "Password"}}},
		{"control", []StatusField{{Key: "oem.sbmc.cid", Label: "CID\x1b"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateStatusFields("sbmc", tc.fields); err == nil {
				t.Fatal("invalid statusFields accepted")
			}
		})
	}
	if err := validateStatusFields("sbmc", []StatusField{model, {Key: "oem.sbmc.cid", Label: "CID"}}); err != nil {
		t.Fatal(err)
	}
}

func TestResolverStatusFieldsRegistry(t *testing.T) {
	for _, tc := range []struct {
		name, registry string
	}{
		{"unregistered", "name,key,sensitive,default\n"},
		{"sensitive", "name,key,sensitive,default\nSBMC_PLATFORM_SECRET,oem.sbmc.secret,true,\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			files := fixturePackage(t, upgradeToolID, "toolplugin", "Fixture")
			editFixtureManifest(t, files, func(m *Manifest) {
				m.Publisher = "sbmc"
				m.EnvRegistry = "environment.csv"
				m.Provides.Resolvers = map[string]Resolver{"sbmc": {
					Entry:        "resolver.exe",
					StatusFields: []StatusField{{Key: "oem.sbmc.secret", Label: "Secret"}},
				}}
			})
			files["environment.csv"] = []byte(tc.registry)
			files["resolver.exe"] = []byte("fixture")
			if _, err := store.Install(writeFixtureZIP(t, files)); err == nil ||
				!strings.Contains(err.Error(), "未授权或未登记") {
				t.Fatalf("install = %v", err)
			}
		})
	}
}
