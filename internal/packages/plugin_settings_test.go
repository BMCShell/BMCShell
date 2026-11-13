package packages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallThemeWithSettings(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fixturePackage(t, bookID, "theme", "Test Theme")
	var manifest Manifest
	if err := json.Unmarshal(files[ManifestFile], &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Data = "themes/.bashrc.perso"
	manifest.Publisher = "bmcshell"
	files[manifest.Data] = []byte("_bmcshell_theme_render() { :; }\n")
	const declarations = `[
		{"key":"pack","label":"话语包","path":"appearance.theme","hint":"选择提示符使用的话语包。","env":"BMCSHELL_THEME_TEST_PACK","type":"enum","default":"plain","options":[{"value":"plain","label":"简洁","hint":"使用简短提示。"},{"value":"friendly","label":"友好"}]},
		{"key":"symbol","label":"提示符号","type":"string","default":""},
		{"key":"offset","label":"偏移","type":"integer","default":0,"min":-3,"max":3},
		{"key":"emoji","label":"显示表情","type":"boolean","default":true}
	]`
	if err := json.Unmarshal([]byte(declarations), &manifest.Settings); err != nil {
		t.Fatal(err)
	}
	files[ManifestFile], err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Install(writeFixtureZIP(t, files)); err != nil {
		t.Fatal(err)
	}
	installed, err := s.Find(bookID)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		key, defaultValue, valid, invalid string
	}{
		{"pack", "plain", "friendly", "missing"},
		{"symbol", "", "你好", "\x1b[31m"},
		{"offset", "0", "-3", "4"},
		{"emoji", "true", "false", "1"},
	}
	if len(installed.Settings) != len(checks) {
		t.Fatalf("installed settings = %d", len(installed.Settings))
	}
	field := installed.Settings[0]
	if field.Path != "appearance.theme" || field.Hint != "选择提示符使用的话语包。" || field.Env != "BMCSHELL_THEME_TEST_PACK" {
		t.Fatalf("installed metadata = %+v", field)
	}
	if got := field.Options[0].Hint; got != "使用简短提示。" {
		t.Fatalf("installed option hint = %q", got)
	}
	for i, check := range checks {
		setting := installed.Settings[i]
		if setting.Key != check.key || setting.DefaultValue() != check.defaultValue {
			t.Errorf("setting %q default = %q, want %q = %q", setting.Key, setting.DefaultValue(), check.key, check.defaultValue)
		}
		if err := setting.Validate(check.valid); err != nil {
			t.Errorf("%s rejects %q: %v", check.key, check.valid, err)
		}
		if err := setting.Validate(check.invalid); err == nil {
			t.Errorf("%s accepts %q", check.key, check.invalid)
		}
	}
	for _, value := range []string{"-4", "1.5", "01", "+1", "-0", "9223372036854775808"} {
		if err := installed.Settings[2].Validate(value); err == nil {
			t.Errorf("integer accepts %q", value)
		}
	}
}

func TestRejectInvalidPluginSettings(t *testing.T) {
	declarations := map[string]string{
		"reserved theme key":     `[{"key":"active","label":"主题","type":"boolean","default":true}]`,
		"duplicate keys":         `[{"key":"a","label":"甲","type":"string","default":""},{"key":"a","label":"乙","type":"string","default":""}]`,
		"invalid key":            `[{"key":"plugin.test","label":"甲","type":"string","default":""}]`,
		"unsafe label":           `[{"key":"a","label":"甲\n乙","type":"string","default":""}]`,
		"invalid path":           `[{"key":"a","label":"甲","path":"appearance..theme","type":"string","default":""}]`,
		"uppercase path":         `[{"key":"a","label":"甲","path":"Appearance.theme","type":"string","default":""}]`,
		"unsafe hint":            `[{"key":"a","label":"甲","hint":"甲\n乙","type":"string","default":""}]`,
		"foreign env":            `[{"key":"a","label":"甲","env":"OTHER_THEME_OPTION","type":"string","default":""}]`,
		"unsafe env":             `[{"key":"a","label":"甲","env":"BMCSHELL_THEME_OPTION=1","type":"string","default":""}]`,
		"reserved bridge env":    `[{"key":"a","label":"甲","env":"BMCSHELL_BRIDGE_INTERACTIVE","type":"string","default":""}]`,
		"reserved resolver env":  `[{"key":"a","label":"甲","env":"BMCSHELL_RESOLVER_LIBRARIES","type":"string","default":""}]`,
		"duplicate env":          `[{"key":"a","label":"甲","env":"BMCSHELL_THEME_OPTION","type":"string","default":""},{"key":"b","label":"乙","env":"BMCSHELL_THEME_OPTION","type":"string","default":""}]`,
		"unsupported type":       `[{"key":"a","label":"甲","type":"number","default":0}]`,
		"missing default":        `[{"key":"a","label":"甲","type":"string"}]`,
		"null default":           `[{"key":"a","label":"甲","type":"string","default":null}]`,
		"string boolean":         `[{"key":"a","label":"甲","type":"boolean","default":"true"}]`,
		"string integer":         `[{"key":"a","label":"甲","type":"integer","default":"1"}]`,
		"fractional integer":     `[{"key":"a","label":"甲","type":"integer","default":1.5}]`,
		"inverted bounds":        `[{"key":"a","label":"甲","type":"integer","default":1,"min":2,"max":0}]`,
		"default outside bounds": `[{"key":"a","label":"甲","type":"integer","default":1,"min":2}]`,
		"enum missing options":   `[{"key":"a","label":"甲","type":"enum","default":"first"}]`,
		"enum unknown default":   `[{"key":"a","label":"甲","type":"enum","default":"other","options":[{"value":"first","label":"第一个"}]}]`,
		"duplicate enum value":   `[{"key":"a","label":"甲","type":"enum","default":"first","options":[{"value":"first","label":"第一个"},{"value":"first","label":"第二个"}]}]`,
		"unsafe option hint":     `[{"key":"a","label":"甲","type":"enum","default":"first","options":[{"value":"first","label":"第一个","hint":"甲\n乙"}]}]`,
		"misplaced options":      `[{"key":"a","label":"甲","type":"boolean","default":true,"options":[]}]`,
		"misplaced bounds":       `[{"key":"a","label":"甲","type":"boolean","default":true,"min":0}]`,
	}
	for name, data := range declarations {
		t.Run(name, func(t *testing.T) {
			manifest := Manifest{Type: "theme", Publisher: "bmcshell", Data: "themes/.bashrc.perso"}
			if err := json.Unmarshal([]byte(data), &manifest.Settings); err != nil {
				t.Fatal(err)
			}
			if err := manifest.validatePluginSettings(); err == nil {
				t.Fatal("accepted invalid declarations")
			}
		})
	}
}

func TestPluginSettingEnvNamespace(t *testing.T) {
	for _, test := range []struct {
		env       string
		publisher string
		want      bool
	}{
		{env: "SBMC_RESOLVER_TIMEOUT_SECONDS", publisher: "sbmc", want: true},
		{env: "SBMC_RESOLVER_TIMEOUT_SECONDS", publisher: "sb"},
		{env: "SBMC_RESOLVER_TIMEOUT_SECONDS"},
		{env: "sbmc_resolver_timeout_seconds", publisher: "sbmc"},
		{env: "BMCSHELL_THEME_ASCII", publisher: "bmcshell", want: true},
		{env: "BASH_XTRACE_FD", publisher: "bash"},
		{env: "LD_LIBRARY_PATH", publisher: "ld"},
		{env: "DYLD_LIBRARY_PATH", publisher: "dyld"},
		{env: "MSYS_NO_PATHCONV", publisher: "msys"},
		{env: "CYGWIN_TEST_OPTION", publisher: "cygwin"},
	} {
		if got := validPluginSettingEnv(test.env, test.publisher); got != test.want {
			t.Errorf("env %q for %q valid = %t, want %t", test.env, test.publisher, got, test.want)
		}
	}
}

func TestSettingsEnvironmentCollisions(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := Package{Manifest: Manifest{ID: bookID, Settings: []PluginSetting{{Env: "SBMC_RESOLVER_TIMEOUT"}}}}
	second := Package{Manifest: Manifest{ID: "0cf9329d-c9de-43ca-9ec7-224b74b05a44", Settings: []PluginSetting{{Env: "SBMC_RESOLVER_TIMEOUT"}}}}
	if _, err := store.Environment([]Package{first}); err != nil {
		t.Fatalf("unique environment mapping: %v", err)
	}
	if _, err := store.Environment([]Package{first, second}); err == nil || !strings.Contains(err.Error(), first.Settings[0].Env) {
		t.Fatalf("duplicate mapping error = %v", err)
	}

	first.Root = t.TempDir()
	first.Publisher = "sbmc"
	first.EnvRegistry = "env.csv"
	csv := "name,key,sensitive,default\nSBMC_RESOLVER_TIMEOUT,oem.sbmc.timeout,false,5\n"
	if err := os.WriteFile(filepath.Join(first.Root, first.EnvRegistry), []byte(csv), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Environment([]Package{first}); err == nil || !strings.Contains(err.Error(), "已在环境变量登记表声明") {
		t.Fatalf("plugin registry conflict error = %v", err)
	}

	second.Settings[0].Env = "BMCSHELL_SNMP_VERSION"
	if _, err := store.Environment([]Package{second}); err == nil || !strings.Contains(err.Error(), "已在环境变量登记表声明") {
		t.Fatalf("framework registry conflict error = %v", err)
	}
}

func TestLegacyThemeCannotDeclareSettings(t *testing.T) {
	manifest := Manifest{Type: "theme", Data: "themes/theme.json"}
	if err := manifest.validatePluginSettings(); err != nil {
		t.Fatal(err)
	}
	manifest.Settings = []PluginSetting{{Key: "emoji", Label: "显示表情", Type: "boolean", Default: json.RawMessage(`true`)}}
	if err := manifest.validatePluginSettings(); err == nil {
		t.Fatal("accepted settings for a theme without a Bash entry")
	}
}

func TestPluginSettingOptionRequiresValue(t *testing.T) {
	var option PluginSettingOption
	if err := json.Unmarshal([]byte(`{"value":"","label":"空值"}`), &option); err != nil || option.Value != "" {
		t.Fatalf("explicit empty value = %+v, %v", option, err)
	}
	for _, data := range []string{`{"label":"空值"}`, `{"value":null,"label":"空值"}`} {
		if err := json.Unmarshal([]byte(data), &option); err == nil {
			t.Errorf("accepted option without a string value: %s", data)
		}
	}
}
