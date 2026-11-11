package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/packages"
)

func TestResolverStatus(t *testing.T) {
	t.Setenv("BMCSHELL_HOME", t.TempDir())
	t.Setenv("BMCSHELL_BRIDGE_PROXY", "")
	t.Setenv("BMCSHELL_SNMP_ENTERPRISES", "")
	t.Setenv("BMCSHELL_BRIDGE_STATUS_PASSWORD", "test-secret")

	const name = "catalog-tool-fixture"
	pkg := catalogManifest("2a000000-0000-4000-8000-000000000009", name, "toolplugin", "1.0.0")
	pkg.EnvRegistry = "environment.csv"
	pkg.Provides.Resolvers = map[string]packages.Resolver{"catalog": {
		Entry: "resolver.exe",
		StatusFields: []packages.StatusField{
			{Key: "target.model", Label: "机型"},
			{Key: "oem.catalog.cid", Label: "CID"},
			{Key: "oem.catalog.sku", Label: "SKU"},
			{Key: "snmp.enterprises", Label: "SNMP 企业编号"},
		},
	}}
	dir := catalogFixture(t, pkg)
	files := map[string]string{
		// status must use the existing session, so this need not be executable.
		"resolver.exe": "fixture",
		"environment.csv": "name,key,sensitive,default\n" +
			"CATALOG_PLATFORM_CID,oem.catalog.cid,false,\n" +
			"CATALOG_PLATFORM_SKU,oem.catalog.sku,false,\n",
	}
	for file, data := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", dir)
	libraryCLI(t, 0, "", "setting", "--set", "BMCSHELL_SNMP_ENTERPRISES", "55")
	const base = "192.0.2.10\n机型：Test Model\nCID：C05\n"
	const ctx = `{"target.ip":"192.0.2.10","target.resolver":"catalog-tool-fixture","target.model":"Test Model","oem.catalog.cid":"C05","snmp.enterprises":"54357"}`
	assertStatus := func(want string) {
		t.Helper()
		if got := libraryCLI(t, 0, "", "status"); got != want {
			t.Fatalf("status = %q, want %q", got, want)
		}
	}

	t.Setenv("BMCSHELL_CONTEXT", ctx)
	assertStatus(base + "SNMP 企业编号：54357\n")
	t.Setenv("BMCSHELL_SNMP_ENTERPRISES", "0")
	assertStatus(base + "SNMP 企业编号：0\n")
	env := libraryCLI(t, 0, "", "status", "--env")
	if !strings.Contains(env, "BMCSHELL_SNMP_ENTERPRISES = 0\n") ||
		!strings.Contains(env, "BMCSHELL_AUTH_PASSWORD = [已隐藏]\n") ||
		strings.Contains(env, "test-secret") || strings.Contains(env, "机型：") {
		t.Fatalf("status --env = %q", env)
	}
	t.Setenv("BMCSHELL_SNMP_ENTERPRISES", "")
	t.Setenv("BMCSHELL_CONTEXT", strings.Replace(ctx, `,"snmp.enterprises":"54357"`, "", 1))
	assertStatus(base + "SNMP 企业编号：55\n")
	t.Setenv("BMCSHELL_CONTEXT", strings.Replace(ctx, name, "catalog", 1))
	assertStatus(base + "SNMP 企业编号：54357\n")
	for _, resolver := range []string{"generic", "removed-resolver"} {
		t.Setenv("BMCSHELL_CONTEXT", strings.Replace(ctx, name, resolver, 1))
		assertStatus("192.0.2.10\n")
	}
	t.Setenv("BMCSHELL_CONTEXT", "")
	assertStatus("尚未选中 BMC 目标。请先运行 bmc use <IP>。\n")
}
