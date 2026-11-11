package cli

import (
	"bytes"
	"strings"
	"testing"

	"bmcshell/internal/envregistry"
	"bmcshell/internal/packages"
	"bmcshell/internal/session"
)

func TestEnvironmentValuePriority(t *testing.T) {
	const name = "DEMO_PLATFORM_MODEL"
	const key = "oem.demo.model"
	framework, plugin := "framework", "plugin"
	registry := envregistry.Registry{name: {Name: name, Key: key, FrameworkDefault: &framework, PluginDefault: &plugin}}
	tool := packages.Tool{EnvReads: []envregistry.Read{{Name: name, Required: true}}}
	for _, tc := range []struct {
		label    string
		ctx      session.Context
		defaults map[string]string
		expected string
	}{
		{"runtime", session.Context{key: "runtime"}, map[string]string{name: "imported"}, "runtime"},
		{"explicit empty", nil, map[string]string{name: ""}, ""},
		{"imported", nil, map[string]string{name: "imported"}, "imported"},
		{"framework", nil, nil, "framework"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			values, err := environmentValues(tool, registry, tc.ctx, tc.defaults)
			if err != nil || values[name] != tc.expected {
				t.Fatalf("values = %v, error = %v", values, err)
			}
		})
	}
	registry[name] = envregistry.Field{Name: name, Key: key, PluginDefault: &plugin}
	values, err := environmentValues(tool, registry, nil, nil)
	if err != nil || values[name] != plugin {
		t.Fatalf("values = %v, error = %v", values, err)
	}
	registry[name] = envregistry.Field{Name: name, Key: key}
	if _, err := environmentValues(tool, registry, nil, nil); err == nil {
		t.Fatal("missing required value was accepted")
	}
}

func TestSharedSNMPEnterprisesEnvironment(t *testing.T) {
	registry, err := envregistry.LoadFramework("")
	if err != nil {
		t.Fatal(err)
	}
	const name = "BMCSHELL_SNMP_ENTERPRISES"
	tool := packages.Tool{EnvReads: []envregistry.Read{{Name: name}}}
	t.Setenv(name, "")
	values, err := environmentValues(tool, registry, session.Context{"snmp.enterprises": "54357"}, nil)
	if err != nil || values[name] != "54357" {
		t.Fatalf("session field = %v, %v", values, err)
	}
	t.Setenv(name, "0")
	values, err = environmentValues(tool, registry, session.Context{"snmp.enterprises": "54357"}, nil)
	if err != nil || values[name] != "0" {
		t.Fatalf("exported field = %v, %v", values, err)
	}
	values, err = environmentValues(packages.Tool{}, registry, nil, nil)
	if err != nil || len(values) != 0 {
		t.Fatalf("undeclared read = %v, %v", values, err)
	}
	t.Setenv(name, "invalid")
	if _, err := environmentValues(tool, registry, nil, nil); err == nil {
		t.Fatal("invalid exported ENTERPRISES accepted")
	}
}

func TestSharedSNMPEnterprisesStatus(t *testing.T) {
	t.Setenv("BMCSHELL_SNMP_ENTERPRISES", "0")
	t.Setenv("BMCSHELL_BRIDGE_PROXY", "")
	store, err := packages.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := app{home: t.TempDir(), store: store, out: &out}
	if err := a.statusEnvironment(session.Context{"snmp.enterprises": "54357"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "BMCSHELL_SNMP_ENTERPRISES = 0") {
		t.Fatalf("status field missing: %s", &out)
	}
}
