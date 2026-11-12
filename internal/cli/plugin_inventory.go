package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
	"bmcshell/internal/render"
	"bmcshell/internal/terminal"
)

func pluginDetailFlag(arg string) bool {
	return arg == "-d" || arg == "--detail"
}

func (a *app) searchPlugins(args []string) error {
	var keyword string
	var detail bool
	for _, arg := range args {
		switch {
		case !detail && pluginDetailFlag(arg):
			detail = true
		case keyword == "" && strings.TrimSpace(arg) != "" && !strings.HasPrefix(arg, "-"):
			keyword = strings.ToLower(strings.TrimSpace(arg))
		default:
			return errors.New("扩展包搜索参数无效。运行 bmc plugin search --help 查看用法")
		}
	}
	if keyword == "" {
		return errors.New("请输入要搜索的包名关键词。运行 bmc plugin search --help 查看用法")
	}

	entries, err := catalog.Open(a.home).Entries()
	if err != nil {
		return err
	}
	matches := newestEntries(entries, func(entry catalog.Entry) bool {
		return strings.Contains(strings.ToLower(entry.Name), keyword)
	})
	display := a.pluginDisplay(detail, pluginSearchView)
	defer display.close()

	installed := make(map[string]packages.Package)
	if display.width > 0 || detail {
		list, _, err := a.store.Inventory()
		if err != nil {
			return err
		}
		for _, p := range list {
			installed[p.ID] = p
		}
	}
	for _, match := range matches {
		p, found := installed[match.ID]
		if found && match.MatchesInstalled(p) {
			display.row(match, true, match.Description, p.Description, p.Summary)
		} else {
			display.row(match, false, match.Description)
		}
	}
	return nil
}

func (a *app) listPluginInventory(kind string, installedOnly, detail bool) error {
	installed, damaged, err := a.store.Inventory()
	if err != nil {
		return err
	}
	if installedOnly {
		display := a.pluginDisplay(detail, pluginInstalledView)
		for _, p := range installed {
			if kind == "" || kind == packages.CanonicalType(p.Type) {
				display.row(installedPluginEntry(p), true, p.Description, p.Summary)
			}
		}
		display.close()
	} else {
		display := a.pluginDisplay(detail, pluginIndexedView)
		err = a.listIndexedPlugins(kind, display, installed)
		display.close()
	}
	for _, issue := range damaged {
		if kind == "" || kind == packages.CanonicalType(issue.Package.Type) {
			fmt.Fprintf(a.out, "已损坏\t%s\n", issue.Error())
		}
	}
	return err
}

func (a *app) listIndexedPlugins(kind string, display pluginDisplay, installed []packages.Package) error {
	unlisted := make(map[string]packages.Package, len(installed))
	for _, p := range installed {
		unlisted[p.ID] = p
	}

	repository := catalog.Open(a.home)
	sources, err := repository.Sources()
	var failures []error
	if err != nil {
		failures = append(failures, err)
	} else if len(sources) == 0 {
		fmt.Fprintln(a.errOut, "尚未配置软件源。请通过 bmc setting 配置。")
	}
	for _, source := range sources {
		entries, err := repository.CachedEntries(source)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if entries == nil {
			fmt.Fprintf(a.errOut, "%s：尚无缓存索引，请运行 bmc plugin update。\n", source.Name)
			continue
		}
		if len(entries) == 0 {
			fmt.Fprintf(a.errOut, "%s：索引中没有扩展包。\n", source.Name)
			continue
		}
		slices.SortStableFunc(entries, func(a, b catalog.Entry) int {
			if order := strings.Compare(a.Name, b.Name); order != 0 {
				return order
			}
			return catalog.CompareVersion(b.Version, a.Version)
		})
		for _, entry := range entries {
			if kind != "" && kind != packages.CanonicalType(entry.Type) {
				continue
			}
			p, found := unlisted[entry.ID]
			if found && entry.MatchesInstalled(p) {
				display.row(entry, true, entry.Description, p.Description, p.Summary)
				delete(unlisted, entry.ID)
			} else {
				display.row(entry, false, entry.Description)
			}
		}
	}
	for _, p := range installed {
		if _, pending := unlisted[p.ID]; !pending || kind != "" && kind != packages.CanonicalType(p.Type) {
			continue
		}
		display.row(installedPluginEntry(p), true, p.Description, p.Summary)
	}
	return errors.Join(failures...)
}

func installedPluginEntry(p packages.Package) catalog.Entry {
	source := p.Source
	if source == "" {
		source = "本地"
	}
	return catalog.Entry{
		ID: p.ID, Name: p.Name, Type: p.Type, Version: p.Version,
		CoreAPI: p.CoreAPI, Platforms: p.Platforms, Source: source,
	}
}

type pluginView uint8

const (
	pluginIndexedView pluginView = iota
	pluginInstalledView
	pluginSearchView

	pluginCompactTableWidth = 44
	pluginFullTableWidth    = 110
)

type pluginDisplay struct {
	out    io.Writer
	view   pluginView
	width  int
	color  bool
	detail bool
	table  *render.TextTable
}

func (a *app) pluginDisplay(detail bool, view pluginView) pluginDisplay {
	width, color := 0, false
	// The Bash adapter can inspect stdout even when Windows sees a Cygwin pipe.
	if a.out == os.Stdout && os.Getenv("TERM") != "dumb" {
		columns, _ := strconv.Atoi(os.Getenv("BMCSHELL_BRIDGE_PLUGIN_COLUMNS"))
		if columns > 0 {
			width, color = columns, os.Getenv("NO_COLOR") == ""
		}
	}
	if width == 0 {
		if tty := terminal.Open(a.in, a.out); tty != nil {
			width = tty.Width
		}
	}
	return newPluginDisplay(a.out, view, width, color, detail)
}

func newPluginDisplay(out io.Writer, view pluginView, width int, color, detail bool) pluginDisplay {
	width = min(width, 512)
	display := pluginDisplay{out: out, view: view, width: width, color: color, detail: detail}
	if width > 0 && detail {
		switch {
		case width < pluginCompactTableWidth:
			display.table = render.NewTextTable(out, width, []string{"详情"}, []int{width})
		case width < pluginFullTableWidth:
			display.table = render.NewTextTable(out, width,
				[]string{"扩展包与描述", "发布信息"}, []int{48, 50})
		default:
			display.table = render.NewTextTable(out, width,
				[]string{"扩展包", "类型", "来源", "API", "平台", "UUID", "描述"},
				[]int{34, 7, 8, 3, 13, 36, 44})
		}
	}
	return display
}

func (d pluginDisplay) close() {
	if d.table != nil {
		d.table.Close()
	}
}

func (d pluginDisplay) row(entry catalog.Entry, installed bool, descriptions ...string) {
	name := entry.Name + "@" + entry.Version
	label := name
	if installed {
		label += " [已安装]"
	}
	kind := packages.CanonicalType(entry.Type)
	description := pluginDescription(descriptions)
	platforms := strings.Join(entry.Platforms, ",")
	if platforms == "" {
		platforms = "不限"
	}
	if d.width == 0 {
		switch {
		case d.view == pluginInstalledView:
			fmt.Fprintf(d.out, "%s\t%s\t%s", kind, entry.Name, entry.Version)
		case d.view == pluginSearchView && !d.detail:
			fmt.Fprintf(d.out, "%s\t%s\t%s", name, kind, entry.Source)
		default:
			fmt.Fprintf(d.out, "%s\t%s\t%s\t%d\t%s\t%s", label, kind, entry.Source, entry.CoreAPI, platforms, entry.ID)
		}
		if d.detail {
			if description == "" {
				description = "-"
			}
			fmt.Fprintf(d.out, "\t%s", description)
		}
		fmt.Fprintln(d.out)
		return
	}

	if description == "" {
		description = "暂无描述"
	}
	if d.table != nil {
		api := strconv.Itoa(entry.CoreAPI)
		switch {
		case d.width < pluginCompactTableWidth:
			d.table.Row([]string{strings.Join([]string{
				label, "类型 " + kind, "来源 " + entry.Source, "API " + api,
				"平台 " + platforms, "UUID " + entry.ID, "描述 " + description,
			}, "\n")})
		case d.width < pluginFullTableWidth:
			metadata := fmt.Sprintf("类型 %s   来源 %s\nAPI %s   平台 %s\nUUID %s", kind, entry.Source, api, platforms, entry.ID)
			d.table.Row([]string{label + "\n" + description, metadata})
		default:
			d.table.Row([]string{label, kind, entry.Source, api, platforms, entry.ID, description})
		}
		return
	}

	d.writeLine(label, "\x1b[1;96m")
	for _, line := range render.WrapText(description, d.width-2) {
		d.writeLine("  "+line, "\x1b[90m")
	}
}

func (d pluginDisplay) writeLine(value, style string) {
	if d.color {
		fmt.Fprintf(d.out, "%s%s\x1b[0m\n", style, value)
	} else {
		fmt.Fprintln(d.out, value)
	}
}

func pluginDescription(descriptions []string) string {
	for _, candidate := range descriptions {
		words := strings.FieldsFunc(candidate, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		})
		if len(words) > 0 {
			return strings.Join(words, " ")
		}
	}
	return ""
}
