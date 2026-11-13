package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"bmcshell/internal/packages"
)

func TestPromptUsesActiveThemePackage(t *testing.T) {
	home := filepath.Join(t.TempDir(), "data with spaces")
	t.Setenv("BMCSHELL_HOME", home)
	t.Setenv("BMCSHELL_CONTEXT", "")
	t.Setenv("BMCSHELL_BRIDGE_THEME_SETTINGS", "")
	theme := catalogManifest("374e74c7-914a-4cd0-ab70-7dfc2670dbe7", "catalog-theme-fixture", "theme", "1.0.2")
	source := catalogFixture(t, theme)
	theme.Data = "themes/.bashrc.perso"
	theme.Settings = []packages.PluginSetting{
		{Key: "blank_line", Label: "命令间留空行", Type: "boolean", Default: json.RawMessage(`true`)},
		{Key: "text", Label: "提示文字", Type: "string", Default: json.RawMessage(`"one=two"`)},
	}
	manifest, err := json.Marshal(theme)
	if err != nil {
		t.Fatal(err)
	}
	write := func(root, rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "bmc-package.json", string(manifest))
	write(source, theme.Data, "_bmcshell_theme_render() { PS1='fixture> '; }\n")
	libraryCLI(t, 0, "", "plugin", "install", "--file", source)
	if got := libraryCLI(t, 0, "", "bridge", "prompt"); got != "" {
		t.Fatalf("prompt before activation = %q", got)
	}
	libraryCLI(t, 0, "", "setting", "--set", "theme.active", theme.Name)
	root := filepath.Join(home, "plugins", "theme", theme.ID, theme.Version)
	path := filepath.Join(root, theme.Data)
	if got := libraryCLI(t, 0, "", "bridge", "prompt"); got != "entry:\t|false\t"+path {
		t.Fatalf("prompt without target = %q", got)
	}
	t.Setenv("BMCSHELL_CONTEXT", `{"target.ip":"192.0.2.10","target.auth_verified":"true"}`)
	if got := libraryCLI(t, 0, "", "bridge", "prompt"); got != "entry:\t192.0.2.10|true\t"+path {
		t.Fatalf("prompt with target = %q", got)
	}
	t.Setenv("BMCSHELL_BRIDGE_THEME_SETTINGS", "1")
	if got := libraryCLI(t, 0, "", "bridge", "prompt"); got != "entry:\t192.0.2.10|true\t"+path+"\nblank_line=true\ntext=one=two" {
		t.Fatalf("prompt with defaults = %q", got)
	}
	libraryCLI(t, 0, "", "setting", "--set", "theme.blank_line", "false")
	if got := libraryCLI(t, 0, "", "bridge", "prompt"); got != "entry:\t192.0.2.10|true\t"+path+"\nblank_line=false\ntext=one=two" {
		t.Fatalf("prompt with saved setting = %q", got)
	}

	libraryCLI(t, 0, "", "setting", "--reset", "theme.active")
	if got := libraryCLI(t, 0, "", "bridge", "prompt"); got != "" {
		t.Fatalf("prompt after reset = %q", got)
	}
}
