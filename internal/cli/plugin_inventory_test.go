package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"bmcshell/internal/catalog"
)

func TestPluginListInventory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BMCSHELL_HOME", home)
	t.Setenv("BMCSHELL_BRIDGE_PROXY", "")
	const id = "2a000000-0000-4000-8000-000000000001"
	const name = "catalog-tool-fixture"
	const localID = "2a000000-0000-4000-8000-000000000002"
	const localName = "catalog-theme-local"
	platform := runtime.GOOS + "-" + runtime.GOARCH
	tool := catalogManifest(id, name, "toolplugin", "1.10.0")
	tool.Platforms = []string{platform}
	tool.Description = "工具 描述"
	archive := archiveUpgradeFixture(t, catalogFixture(t, tool))

	current := catalog.Entry{
		ID: id, Name: name, Type: "toolplugin", Version: "1.10.0", CoreAPI: 1,
		Platforms: tool.Platforms, URL: "/tool.zip?artifact-token=secret", SHA256: fmt.Sprintf("%x", sha256.Sum256(archive)),
	}
	old := current
	old.Version, old.Platforms = "1.2.0", nil
	old.Description = "历史\t版本\n说明\x1b"
	foreign := current
	foreign.Platforms = []string{"fixture-arch"}
	future := current
	future.CoreAPI = 2

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		entries := []catalog.Entry{}
		switch req.URL.Path {
		case "/official":
			entries = []catalog.Entry{old, current, foreign, future}
		case "/team":
			entries = []catalog.Entry{current}
		case "/tool.zip":
			_, _ = w.Write(archive)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema": 1, "packages": entries})
	}))
	defer server.Close()
	libraryCLI(t, 0, "", "setting", "--set", "plugin.source.official", server.URL+"/official?index-token=secret")
	libraryCLI(t, 0, "", "plugin", "update")
	libraryCLI(t, 0, "", "plugin", "install", name+"@1.10.0")
	for _, source := range []string{"team", "empty"} {
		libraryCLI(t, 0, "", "setting", "--set", "plugin.source."+source, server.URL+"/"+source+"?index-token=secret")
	}
	libraryCLI(t, 0, "", "plugin", "update")
	server.Close()
	libraryCLI(t, 0, "", "setting", "--set", "plugin.source.pending", server.URL+"/pending")

	local := catalogManifest(localID, localName, "theme", "1.0.0")
	local.ShortName, local.Data, local.Summary = "local", "theme.json", "本地 主题"
	libraryCLI(t, 0, "", "plugin", "install", "--file", catalogFixture(t, local))

	installed := name + "@1.10.0 [已安装]\ttool\tofficial\t1\t" + platform + "\t" + id
	otherPlatform := name + "@1.10.0\ttool\tofficial\t1\tfixture-arch\t" + id
	otherAPI := name + "@1.10.0\ttool\tofficial\t2\t" + platform + "\t" + id
	oldVersion := name + "@1.2.0\ttool\tofficial\t1\t不限\t" + id
	team := name + "@1.10.0\ttool\tteam\t1\t" + platform + "\t" + id
	localRow := localName + "@1.0.0 [已安装]\ttheme\t本地\t1\t不限\t" + localID
	official := strings.Join([]string{installed, otherPlatform, otherAPI, oldVersion, ""}, "\n")
	all := official + team + "\n" + localRow + "\n"
	var out, errs bytes.Buffer
	if code := Run("test", []string{"plugin", "list"}, strings.NewReader(""), &out, &errs); code != 0 || out.String() != all {
		t.Fatalf("list status = %d, stdout = %q, stderr = %q", code, out.String(), errs.String())
	}
	if got := errs.String(); !strings.Contains(got, "empty：索引中没有扩展包") ||
		!strings.Contains(got, "pending：尚无缓存索引") || strings.Contains(got+out.String(), "secret") {
		t.Fatalf("list diagnostics = %q", got)
	}
	detailed := strings.Join([]string{
		installed + "\t工具 描述", otherPlatform + "\t-", otherAPI + "\t-", oldVersion + "\t历史 版本 说明",
		team + "\t-", localRow + "\t本地 主题", "",
	}, "\n")
	if got := libraryCLI(t, 0, "", "plugin", "list", "--detail"); got != detailed {
		t.Fatalf("detailed list = %q", got)
	}
	if got := libraryCLI(t, 0, "", "plugin", "list", "tool"); got != official+team+"\n" {
		t.Fatalf("tool list = %q", got)
	}
	if got := libraryCLI(t, 0, "", "plugin", "list", "--installed", "-d", "theme"); got != "theme\t"+localName+"\t1.0.0\t本地 主题\n" {
		t.Fatalf("detailed installed theme list = %q", got)
	}
	for _, flag := range []string{"-d", "--detail"} {
		got := libraryCLI(t, 0, "", "plugin", "search", name, flag)
		if !strings.Contains(got, installed+"\t工具 描述") || strings.Contains(got, "@1.2.0") {
			t.Fatalf("detailed search with %s = %q", flag, got)
		}
	}
	if got := libraryCLI(t, 0, "", "plugin", "search", "--detail", name); !strings.Contains(got, id) {
		t.Fatalf("search with option before keyword = %q", got)
	}
	libraryCLI(t, 1, "", "plugin", "search", "--detail")
	libraryCLI(t, 1, "", "plugin", "search", name, "-d", "--detail")
	libraryCLI(t, 1, "", "plugin", "list", "--detial")
	libraryCLI(t, 1, "", "plugin", "search", name, "--detial")
	libraryCLI(t, 1, "", "plugin", "catalog")

	libraryCLI(t, 0, "", "setting", "--set", "plugin.source.team", server.URL+"/changed")
	if got := libraryCLI(t, 0, "", "plugin", "list"); got != official+localRow+"\n" {
		t.Fatalf("changed source exposed its old index: %q", got)
	}
	if err := os.WriteFile(filepath.Join(home, "catalog", "index-team.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := libraryCLI(t, 1, "", "plugin", "list"); got != official+localRow+"\n" {
		t.Fatalf("corrupt cache hid other indexes: %q", got)
	}
	if got := libraryCLI(t, 0, "", "plugin", "list", "tool", "--installed"); got != "tool\t"+name+"\t1.10.0\n" {
		t.Fatalf("corrupt cache interfered with the installed list: %q", got)
	}
	libraryCLI(t, 0, "", "setting", "--reset", "plugin.source.official")
	if got := libraryCLI(t, 1, "", "plugin", "list"); got != installed+"\n"+localRow+"\n" {
		t.Fatalf("removed source hid an installed package: %q", got)
	}
}

func TestCompactPluginDisplay(t *testing.T) {
	var output bytes.Buffer
	display := newPluginDisplay(&output, pluginIndexedView, 28, true, false)
	entry := catalog.Entry{Name: "fixture-tool-demo", Version: "1.0.0"}
	display.row(entry, true, "显示\t当前\n设备\x1b")
	display.row(entry, false)
	want := "\x1b[1;96mfixture-tool-demo@1.0.0 [已安装]\x1b[0m\n" +
		"\x1b[90m  显示 当前 设备\x1b[0m\n" +
		"\x1b[1;96mfixture-tool-demo@1.0.0\x1b[0m\n" +
		"\x1b[90m  暂无描述\x1b[0m\n"
	if output.String() != want {
		t.Fatalf("compact display = %q, want %q", output.String(), want)
	}
}
