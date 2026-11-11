package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
)

func (a *app) pluginUpdate() error {
	repository, err := a.catalog()
	if err != nil {
		return err
	}
	results, err := repository.Refresh(context.Background(), "")
	for _, result := range results {
		if result.Err == nil {
			fmt.Fprintf(a.out, "%s: %d indexed package versions\n", result.Source, result.Count)
		}
	}
	return err
}

func (a *app) pluginSearch(keyword string) error {
	entries, err := catalog.Open(a.home).Entries()
	if err != nil {
		return err
	}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return errors.New("search requires a keyword")
	}
	matches := newestEntries(entries, func(entry catalog.Entry) bool {
		return strings.Contains(strings.ToLower(entry.Name), keyword)
	})
	for _, match := range matches {
		fmt.Fprintf(a.out, "%s@%s\t%s\t%s\n", match.Name, match.Version, packages.CanonicalType(match.Type), match.Source)
	}
	return nil
}

func newestEntries(entries []catalog.Entry, include func(catalog.Entry) bool) []catalog.Entry {
	newest := make(map[string]catalog.Entry)
	for _, entry := range entries {
		if current, found := newest[entry.ID]; include(entry) &&
			(!found || catalog.CompareVersion(entry.Version, current.Version) > 0) {
			newest[entry.ID] = entry
		}
	}
	matches := make([]catalog.Entry, 0, len(newest))
	for _, id := range sortedKeys(newest) {
		matches = append(matches, newest[id])
	}
	slices.SortStableFunc(matches, func(a, b catalog.Entry) int {
		return strings.Compare(a.Name, b.Name)
	})
	return matches
}

func (a *app) catalog() (*catalog.Store, error) {
	address, err := a.proxyAddress()
	if err != nil {
		return nil, err
	}
	return catalog.OpenWithProxy(a.home, address), nil
}

func (a *app) pluginFromCatalog(selector string, upgrading bool) error {
	repository, err := a.catalog()
	if err != nil {
		return err
	}
	entries, err := repository.Entries()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("no compatible catalog entries; configure a source with bmc setting and run bmc plugin update")
	}
	installed, err := a.store.List()
	if err != nil {
		return err
	}
	input, interactive := bufio.NewReader(a.in), terminalInput(a.in)
	var plan []catalog.Entry
	if upgrading {
		plan, err = a.planCatalogUpgrade(input, interactive, entries, installed, selector)
	} else {
		plan, err = a.planCatalogInstall(input, interactive, entries, installed, selector)
	}
	if err != nil || len(plan) == 0 {
		return err
	}
	a.printCatalogPlan(plan, installed)
	if upgrading && interactive {
		fmt.Fprint(a.out, "Apply this upgrade [y/N]? ")
		if choice := readChoice(input); choice != "y" && choice != "yes" {
			fmt.Fprintln(a.out, "Upgrade canceled.")
			return nil
		}
	}
	return a.applyCatalogPlan(repository, plan, installed, input, interactive)
}

func (a *app) planCatalogInstall(input *bufio.Reader, interactive bool, entries []catalog.Entry, installed []packages.Package, selector string) ([]catalog.Entry, error) {
	name, version, pinned := strings.Cut(selector, "@")
	if name == "" || pinned && packages.ValidateVersion(version) != nil {
		return nil, errors.New("usage: bmc plugin install <name[@version]>")
	}
	matches := newestEntries(entries, func(entry catalog.Entry) bool {
		return entry.Name == name && (!pinned || entry.Version == version)
	})
	if len(matches) == 0 {
		return nil, fmt.Errorf("package %q was not found; see bmc plugin search", selector)
	}
	var selected int
	if len(matches) > 1 {
		for i, match := range matches {
			fmt.Fprintf(a.out, "%d. %s@%s (%s, %s)\n", i+1, match.Name, match.Version, packages.CanonicalType(match.Type), match.Source)
		}
		if !interactive {
			return nil, fmt.Errorf("ambiguous package name %q; select the package in an interactive terminal", selector)
		}
		fmt.Fprint(a.out, "Select a package number (empty to cancel): ")
		value, err := strconv.Atoi(readChoice(input))
		if err != nil || value < 1 || value > len(matches) {
			return nil, errors.New("package selection canceled or invalid")
		}
		selected = value - 1
	}
	target := matches[selected]
	for _, p := range installed {
		if p.ID == target.ID && p.Type == "library" && p.Version == target.Version {
			fmt.Fprintf(a.out, "%s %s is already installed.\n", p.Name, p.Version)
			return nil, nil
		}
	}
	if pinned {
		entries = slices.DeleteFunc(slices.Clone(entries), func(entry catalog.Entry) bool {
			return entry.ID == target.ID && entry.Version != version
		})
	}
	return catalog.ResolveInstall(entries, installed, []string{target.ID}, target.Source)
}

func (a *app) planCatalogUpgrade(input *bufio.Reader, interactive bool, entries []catalog.Entry, installed []packages.Package, name string) ([]catalog.Entry, error) {
	var ids []string
	if name != "" {
		p, err := a.choosePackageFrom(input, name, interactive)
		if err != nil {
			return nil, err
		}
		ids = append(ids, p.ID)
	}
	for _, p := range installed {
		if len(ids) > 0 && p.ID != ids[0] || p.Library == nil {
			continue
		}
		for _, entry := range entries {
			if entry.ID == p.ID && entry.LibrarySchemaVersion != p.Library.SchemaVersion &&
				entry.LibraryMigrations[strconv.Itoa(p.Library.SchemaVersion)] == "" && catalog.CompareVersion(entry.Version, p.Version) > 0 {
				fmt.Fprintf(a.out, "%s: release %s requires a different Library schema; incompatible versions are skipped.\n", p.Name, entry.Version)
				break
			}
		}
	}
	plan, err := catalog.ResolveUpgrade(entries, installed, ids, "")
	if err == nil && len(plan) == 0 {
		fmt.Fprintln(a.out, "No upgrades available.")
	}
	return plan, err
}

func (a *app) printCatalogPlan(plan []catalog.Entry, installed []packages.Package) {
	versions := make(map[string]string, len(installed))
	for _, p := range installed {
		versions[p.ID] = p.Version
	}
	fmt.Fprintln(a.out, "Package plan")
	for _, entry := range plan {
		if current := versions[entry.ID]; current != "" {
			fmt.Fprintf(a.out, "  Upgrade %s %s → %s (%s)\n", entry.Name, current, entry.Version, entry.Source)
		} else {
			fmt.Fprintf(a.out, "  Install %s %s (%s)\n", entry.Name, entry.Version, entry.Source)
		}
	}
}

func (a *app) applyCatalogPlan(repository *catalog.Store, plan []catalog.Entry, installed []packages.Package, input *bufio.Reader, interactive bool) error {
	stage, err := os.MkdirTemp("", "bmcshell-catalog-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	files := make([]string, 0, len(plan))
	origins := make(map[string]string, len(plan))
	for _, entry := range plan {
		file, err := repository.Download(context.Background(), entry, stage)
		if err != nil {
			return fmt.Errorf("%s %s from %s: %w", entry.Name, entry.Version, entry.Source, err)
		}
		files = append(files, file)
		origins[entry.ID] = entry.Source
	}
	prepared, err := a.store.PrepareChanges(files, installed)
	if err != nil {
		return err
	}
	defer prepared.Close()
	if err := prepared.SetSources(origins); err != nil {
		return err
	}
	if err := a.commitCatalogPlan(prepared, input, interactive); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Activated %d package changes.\n", len(prepared.ChangedPackages()))
	return nil
}
