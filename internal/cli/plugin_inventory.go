package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
)

func (a *app) listPluginInventory(kind string, installedOnly, detail bool) error {
	installed, damaged, err := a.store.Inventory()
	if err != nil {
		return err
	}
	if installedOnly {
		for _, p := range installed {
			packageType := packages.CanonicalType(p.Type)
			if kind == "" || kind == packageType {
				fmt.Fprintf(a.out, "%s\t%s\t%s", packageType, p.Name, p.Version)
				finishPluginListRow(a.out, detail, p.Description, p.Summary)
			}
		}
	} else {
		err = a.listIndexedPlugins(kind, detail, installed)
	}
	for _, issue := range damaged {
		if kind == "" || kind == packages.CanonicalType(issue.Package.Type) {
			fmt.Fprintf(a.out, "已损坏\t%s\n", issue.Error())
		}
	}
	return err
}

func (a *app) listIndexedPlugins(kind string, detail bool, installed []packages.Package) error {
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
				a.printPluginIndexRow(entry, true)
				finishPluginListRow(a.out, detail, entry.Description, p.Description, p.Summary)
				delete(unlisted, entry.ID)
			} else {
				a.printPluginIndexRow(entry, false)
				finishPluginListRow(a.out, detail, entry.Description)
			}
		}
	}
	for _, p := range installed {
		if _, pending := unlisted[p.ID]; !pending || kind != "" && kind != packages.CanonicalType(p.Type) {
			continue
		}
		source := p.Source
		if source == "" {
			source = "本地"
		}
		a.printPluginIndexRow(catalog.Entry{
			ID: p.ID, Name: p.Name, Type: p.Type, Version: p.Version,
			CoreAPI: p.CoreAPI, Platforms: p.Platforms, Source: source,
		}, true)
		finishPluginListRow(a.out, detail, p.Description, p.Summary)
	}
	return errors.Join(failures...)
}

func (a *app) printPluginIndexRow(entry catalog.Entry, installed bool) {
	name := entry.Name + "@" + entry.Version
	if installed {
		name += " [已安装]"
	}
	platforms := strings.Join(entry.Platforms, ",")
	if platforms == "" {
		platforms = "不限"
	}
	fmt.Fprintf(a.out, "%s\t%s\t%s\t%d\t%s\t%s",
		name, packages.CanonicalType(entry.Type), entry.Source, entry.CoreAPI, platforms, entry.ID)
}

func finishPluginListRow(out io.Writer, detail bool, descriptions ...string) {
	if detail {
		description := "-"
		for _, candidate := range descriptions {
			words := strings.FieldsFunc(candidate, func(r rune) bool {
				return unicode.IsSpace(r) || unicode.IsControl(r)
			})
			if len(words) > 0 {
				description = strings.Join(words, " ")
				break
			}
		}
		fmt.Fprintf(out, "\t%s", description)
	}
	fmt.Fprintln(out)
}
