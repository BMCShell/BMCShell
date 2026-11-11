package cli

import (
	"testing"

	"bmcshell/internal/packages"
	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

func TestBookUsesSharedSNMPField(t *testing.T) {
	directory := t.TempDir()
	store, err := packages.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	a := app{home: directory, store: store}
	if err := settings.Save(directory, settings.Config{EnvDefaults: map[string]string{"BMCSHELL_SNMP_ENTERPRISES": "10"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BMCSHELL_SNMP_ENTERPRISES", "")
	t.Setenv("BMCSHELL_AUTH_PASSWORD", "fixture-private")
	_, values, err := a.bookQueryEnvironment(session.Context{"snmp.enterprises": "20", "auth.password": "fixture-private"})
	if err != nil || values["BMCSHELL_SNMP_ENTERPRISES"] != "20" {
		t.Fatalf("Book session enterprise = %q, %v", values["BMCSHELL_SNMP_ENTERPRISES"], err)
	}
	if _, exists := values["BMCSHELL_AUTH_PASSWORD"]; exists {
		t.Fatal("private field exposed to Book")
	}
	t.Setenv("BMCSHELL_SNMP_ENTERPRISES", "0")
	_, values, err = a.bookQueryEnvironment(session.Context{"snmp.enterprises": "20"})
	if err != nil || values["BMCSHELL_SNMP_ENTERPRISES"] != "0" {
		t.Fatalf("Book explicit enterprise = %q, %v", values["BMCSHELL_SNMP_ENTERPRISES"], err)
	}
	t.Setenv("BMCSHELL_SNMP_ENTERPRISES", "")
	_, values, err = a.bookQueryEnvironment(nil)
	if err != nil || values["BMCSHELL_SNMP_ENTERPRISES"] != "10" {
		t.Fatalf("Book configured enterprise = %q, %v", values["BMCSHELL_SNMP_ENTERPRISES"], err)
	}
}
