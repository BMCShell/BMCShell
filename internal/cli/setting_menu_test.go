package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
	"bmcshell/internal/settings"
)

func TestSettingsMenu(t *testing.T) {
	home := t.TempDir()
	store, err := packages.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	input := strings.NewReader(strings.Join([]string{
		"99", "4", "1", "3", "1", "4", "0",
		"2", "2", "1", "invalid-ip", "2001:db8::1", "0",
		"3", "1", "1", "0",
		"5", "1", "team", "https://10.0.0.1/index.json?token=hidden-value", "2",
		"1", "1", "https://10.0.0.2/index.json?token=hidden-value",
		"1", "2", "1", "0", "0", "1",
	}, "\n"))
	a := app{home: home, store: store, in: input, out: &out, errOut: &errOut}
	if err := a.settingsMenu(); err != nil {
		t.Fatal(err)
	}

	cfg, err := settings.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got, exists := cfg.EnvDefaults["BMCSHELL_SNMP_VERSION"]; exists {
		t.Fatalf("saved SNMP version = %q", got)
	}
	if got := cfg.EnvDefaults["BMCSHELL_TARGET_IP"]; got != "2001:db8::1" {
		t.Errorf("saved target IP = %q", got)
	}
	if got := cfg.EnvDefaults["BMCSHELL_TLS_MODE"]; got != "verify" {
		t.Errorf("saved TLS mode = %q", got)
	}
	sources, err := catalog.Open(home).Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].URL != "https://10.0.0.2/index.json?token=hidden-value" || sources[0].InsecureTLS {
		t.Fatalf("unexpected saved software source: %+v", sources)
	}
	for _, label := range []string{"目标与认证", "SNMP 版本    2c", "SNMP 版本    3", "添加软件源", "TLS 证书校验    跳过证书校验"} {
		if !strings.Contains(out.String(), label) {
			t.Errorf("missing menu label %q", label)
		}
	}
	for _, key := range []string{"BMCSHELL_AUTH_PASSWORD", "BMCSHELL_SNMP_COMMUNITY", "plugin.source."} {
		if strings.Contains(out.String(), key) {
			t.Errorf("unexpected setting key %q in menu output", key)
		}
	}
	if !strings.Contains(out.String(), "请输入菜单中的编号") || !strings.Contains(out.String(), "必须是 IPv4 或 IPv6 地址") {
		t.Errorf("missing validation feedback: %s", &out)
	}
}

func TestThemeSettingsMenu(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	first := catalogManifest("b6a6bf35-d532-4af1-9b6b-9c96853f7dad", "catalog-theme-first", "theme", "1.0.0")
	first.ShortName, first.DisplayName, first.Data = "first", "第一主题", ".bashrc.perso"
	minimum, maximum := int64(1), int64(8)
	first.Settings = []packages.PluginSetting{
		{Key: "style", Label: "符号样式", Type: "enum", Default: json.RawMessage(`"auto"`), Options: []packages.PluginSettingOption{
			{Value: "auto", Label: "自动"}, {Value: "ascii", Label: "ASCII"},
		}},
		{Key: "width", Label: "显示宽度", Type: "integer", Default: json.RawMessage(`4`), Min: &minimum, Max: &maximum},
	}
	second := catalogManifest("46d820bd-12ce-4149-9679-e9ab55b2d2ad", "catalog-theme-second", "theme", "1.0.0")
	second.ShortName, second.DisplayName, second.Data = "second", "第二主题", ".bashrc.perso"
	second.Settings = []packages.PluginSetting{
		{Key: "style", Label: "额外样式", Type: "boolean", Default: json.RawMessage(`true`)},
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", catalogFixture(t, first), catalogFixture(t, second))
	store, err := packages.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	input := strings.NewReader(strings.Join([]string{
		"1", "1", "2", // Activate the first theme.
		"2", "1", // Enable prompt override.
		"3", "2", // Select ASCII.
		"4", "1", "9", "7", // Reject the out-of-range integer and retry.
		"1", "3", // Switch to the second theme in the same menu.
		"3", "2", // Save false for the same key on the second theme.
		"1", "2", // Return to the first theme.
		"0", "0", "1",
	}, "\n"))
	a := app{home: home, store: store, in: input, out: &out, errOut: &errOut}
	if err := a.settingsMenu(); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"终端主题    第一主题", "覆盖 Bash 提示符    开启", "符号样式    自动", "符号样式    ASCII", "显示宽度    7", "额外样式    开启", "额外样式    关闭"} {
		if !strings.Contains(out.String(), label) {
			t.Errorf("missing menu label %q", label)
		}
	}
	if !strings.Contains(out.String(), "须小于或等于 8") {
		t.Errorf("missing range validation: %s", &out)
	}
	firstThemeIndex := strings.LastIndex(out.String(), "终端主题    第一主题")
	if firstThemeIndex < 0 {
		t.Fatalf("first theme missing after switching: %s", &out)
	}
	firstThemeView := out.String()[firstThemeIndex:]
	if !strings.Contains(firstThemeView, "符号样式    ASCII") || strings.Contains(firstThemeView, "额外样式") {
		t.Errorf("incorrect appearance menu after switching back: %s", firstThemeView)
	}
	if got := libraryCLI(t, 0, "", "setting", "--get", "theme.override"); got != "true\n" {
		t.Fatalf("saved override = %q", got)
	}
	if got := libraryCLI(t, 0, "", "setting", "--get", "theme.style"); got != "ascii\n" {
		t.Fatalf("first theme saved style = %q", got)
	}
	libraryCLI(t, 1, "", "setting", "--set", "theme.style", "unknown")
	libraryCLI(t, 0, "", "setting", "--reset", "theme.style")
	if got := libraryCLI(t, 0, "", "setting", "--get", "theme.style"); got != "auto\n" {
		t.Fatalf("first theme reset style = %q", got)
	}
	libraryCLI(t, 0, "", "setting", "--set", "theme.active", second.Name)
	if got := libraryCLI(t, 0, "", "setting", "--get", "theme.style"); got != "false\n" {
		t.Fatalf("second theme saved style = %q", got)
	}
	libraryCLI(t, 1, "", "setting", "--get", "theme.width")

	// Emulate a value retained after a theme changes its declaration.
	if err := settings.Update(home, func(cfg *settings.Config) error {
		cfg.PluginSettings[second.ID]["style"] = "invalid"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := libraryCLI(t, 0, "", "setting", "--get", "theme.style"); got != "true\n" {
		t.Fatalf("invalid stored value should fall back to default, got %q", got)
	}
	libraryCLI(t, 1, "", "setting", "--check")
	libraryCLI(t, 0, "", "setting", "--reset", "theme.style")
	libraryCLI(t, 0, "", "setting", "--check")
	libraryCLI(t, 0, "", "setting", "--reset")
	cfg, err := settings.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "" || len(cfg.ThemeSettings) != 0 || len(cfg.PluginSettings) != 0 {
		t.Errorf("reset left theme settings: %+v", cfg)
	}
}

func TestSettingsMenuResetsMissingTheme(t *testing.T) {
	home := t.TempDir()
	if err := settings.Save(home, settings.Config{Theme: "missing-theme"}); err != nil {
		t.Fatal(err)
	}
	store, err := packages.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	a := app{home: home, store: store, in: strings.NewReader("1\n1\n2\n0\n0\n1\n"), out: &out, errOut: &errOut}
	if err := a.settingsMenu(); err != nil {
		t.Fatal(err)
	}
	cfg, err := settings.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "" || errOut.Len() != 0 {
		t.Fatalf("theme = %q, errors = %s", cfg.Theme, &errOut)
	}
	for _, label := range []string{"终端主题    不可用", "关闭主题", "终端主题    关闭主题"} {
		if !strings.Contains(out.String(), label) {
			t.Errorf("missing menu label %q", label)
		}
	}
}

func TestSettingsMenuRecoversOrDiscardsDraft(t *testing.T) {
	home := t.TempDir()
	store, err := packages.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "result")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settingResultEnv, marker)
	run := func(input string) string {
		t.Helper()
		var out, errs bytes.Buffer
		a := app{home: home, store: store, in: strings.NewReader(input), out: &out, errOut: &errs}
		if err := a.settingsMenu(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	assertVersion := func(want string) {
		t.Helper()
		cfg, err := settings.Load(home)
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.EnvDefaults["BMCSHELL_SNMP_VERSION"]; got != want {
			t.Fatalf("saved SNMP version = %q, want %q", got, want)
		}
	}
	run("4\n1\n3\n") // EOF after editing leaves the official value unchanged.
	assertVersion("")
	if out := run("1\n0\n2\n"); !strings.Contains(out, "继续编辑草稿") {
		t.Fatalf("missing recovery menu: %s", out)
	}
	assertVersion("")
	if markerData, err := os.ReadFile(marker); err != nil || len(markerData) != 0 {
		t.Fatalf("discard reported a commit: %q, %v", markerData, err)
	}
	run("4\n1\n3\n")
	run("1\n0\n1\n")
	assertVersion("3")
	if markerData, err := os.ReadFile(marker); err != nil || string(markerData) != "committed\n" {
		t.Fatalf("save did not report a commit: %q, %v", markerData, err)
	}
	if _, err := os.Stat(filepath.Join(home, "settings.draft.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("draft remains after save: %v", err)
	}
}
