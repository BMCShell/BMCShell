package cli

import (
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
