package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
)

func (a *app) pluginSource(args []string) error {
	sources := catalog.Open(a.home)
	switch {
	case len(args) == 0 || len(args) == 1 && args[0] == "list":
		list, err := sources.Sources()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(a.out, "No software sources configured.")
		}
		for _, source := range list {
			kind := "custom"
			if source.Official {
				kind = "official"
			}
			index, err := url.Parse(source.URL)
			if err != nil {
				return fmt.Errorf("invalid index URL for source %s", source.Name)
			}
			index.RawQuery, index.ForceQuery = "", false
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", source.Name, kind, index.Redacted())
		}
		return nil
	case len(args) == 3 && args[0] == "add":
		return sources.SetSource(catalog.Source{Name: args[1], URL: args[2]})
	case len(args) == 2 && args[0] == "set-official":
		return sources.SetSource(catalog.Source{Name: "official", URL: args[1], Official: true})
	case len(args) == 2 && args[0] == "remove":
		return sources.RemoveSource(args[1])
	default:
		return errors.New("usage: bmc plugin source list|add <name> <index-url>|set-official <index-url>|remove <name>")
	}
}

func (a *app) pluginUpdate(args []string) error {
	var source string
	if len(args) > 1 || len(args) == 1 && strings.HasPrefix(args[0], "--") {
		return errors.New("usage: bmc plugin update [source]")
	}
	if len(args) == 1 {
		source = args[0]
	}
	results, err := catalog.Open(a.home).Refresh(context.Background(), source)
	for _, result := range results {
		if result.Err == nil {
			fmt.Fprintf(a.out, "%s: %d indexed package versions\n", result.Source, result.Count)
		}
	}
	return err
}

type catalogOptions struct {
	selectors []string
	source    string
	yes       bool
	dryRun    bool
}

func parseCatalogOptions(args []string) (catalogOptions, error) {
	var options catalogOptions
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "--source":
			if options.source != "" || i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return options, errors.New("--source needs one source name")
			}
			i++
			options.source = args[i]
		case "--yes":
			options.yes = true
		case "--dry-run":
			options.dryRun = true
		default:
			if strings.HasPrefix(arg, "-") {
				return options, fmt.Errorf("unknown package option %q", arg)
			}
			options.selectors = append(options.selectors, arg)
		}
	}
	return options, nil
}

func (a *app) pluginFromCatalog(args []string, upgrading bool) error {
	options, err := parseCatalogOptions(args)
	if err != nil {
		return err
	}
	if !upgrading && len(options.selectors) == 0 {
		return errors.New("usage: bmc plugin install <ID-or-name> ... [--source <name>] or --file <directory-or-zip>")
	}
	repository := catalog.Open(a.home)
	if options.source != "" {
		list, err := repository.Sources()
		if err != nil {
			return err
		}
		var found bool
		for _, source := range list {
			found = found || source.Name == options.source
		}
		if !found {
			return fmt.Errorf("unknown software source %q", options.source)
		}
	}
	entries, err := repository.Entries()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("no compatible catalog entries; configure a source and run bmc plugin update")
	}
	installed, err := a.store.List()
	if err != nil {
		return err
	}
	input := bufio.NewReader(a.in)
	var plan []catalog.Entry
	if upgrading {
		plan, err = a.planCatalogUpgrade(input, entries, installed, options)
	} else {
		plan, err = a.planCatalogInstall(input, entries, installed, options)
	}
	if err != nil || len(plan) == 0 {
		return err
	}
	a.printCatalogPlan(plan, installed)
	if options.dryRun {
		return nil
	}
	if upgrading && !options.yes {
		fmt.Fprint(a.out, "Apply this upgrade [y/N]? ")
		switch readChoice(input) {
		case "y", "yes":
		default:
			fmt.Fprintln(a.out, "Upgrade canceled.")
			return nil
		}
	}
	return a.applyCatalogPlan(plan, installed)
}

func (a *app) planCatalogInstall(input *bufio.Reader, entries []catalog.Entry, installed []packages.Package, options catalogOptions) ([]catalog.Entry, error) {
	ids := make([]string, 0, len(options.selectors))
	for _, selector := range options.selectors {
		matches, err := catalog.Match(entries, selector, options.source)
		if err != nil {
			return nil, err
		}
		if len(matches) > 1 {
			if options.yes || options.dryRun {
				var ids []string
				for _, match := range matches {
					ids = append(ids, fmt.Sprintf("%s (%s, %s)", match.ID, match.Source, match.Version))
				}
				return nil, fmt.Errorf("ambiguous package name %q; use a UUID from %s", selector, strings.Join(ids, ", "))
			}
			for i, entry := range matches {
				fmt.Fprintf(a.out, "%d. %s %s (%s, %s)\n", i+1, entry.Name, entry.Version, entry.Source, entry.ID)
			}
			fmt.Fprint(a.out, "Select a package number (empty to cancel): ")
			selected, err := strconv.Atoi(readChoice(input))
			if err != nil || selected < 1 || selected > len(matches) {
				return nil, errors.New("package selection canceled or invalid")
			}
			selector = matches[selected-1].ID
		}
		ids = append(ids, selector)
	}
	return catalog.ResolveInstall(entries, installed, ids, options.source)
}

func (a *app) planCatalogUpgrade(input *bufio.Reader, entries []catalog.Entry, installed []packages.Package, options catalogOptions) ([]catalog.Entry, error) {
	selectors := options.selectors
	for i, selector := range selectors {
		p, err := a.choosePackageFrom(input, selector, !options.yes && !options.dryRun)
		if err != nil {
			return nil, err
		}
		selectors[i] = p.ID
	}
	if len(selectors) == 0 {
		candidates, err := catalog.UpgradeCandidates(entries, installed, options.source)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			fmt.Fprintln(a.out, "No upgrades available.")
			return nil, nil
		}
		if !options.yes && !options.dryRun {
			for i, entry := range candidates {
				fmt.Fprintf(a.out, "%d. %s %s (%s, %s)\n", i+1, entry.Name, entry.Version, entry.Source, entry.ID)
			}
			fmt.Fprint(a.out, "Upgrade one package number, all [a], or cancel [N]: ")
			switch choice := readChoice(input); choice {
			case "a", "all":
			case "", "n", "no":
				return nil, nil
			default:
				selected, err := strconv.Atoi(choice)
				if err != nil || selected < 1 || selected > len(candidates) {
					return nil, errors.New("invalid upgrade selection")
				}
				selectors = []string{candidates[selected-1].ID}
			}
		}
	}
	plan, err := catalog.ResolveUpgrade(entries, installed, selectors, options.source)
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
			fmt.Fprintf(a.out, "  Upgrade %s %s → %s (%s, %s)\n", entry.Name, current, entry.Version, entry.Source, entry.ID)
		} else {
			fmt.Fprintf(a.out, "  Install %s %s (%s, %s)\n", entry.Name, entry.Version, entry.Source, entry.ID)
		}
	}
}

func (a *app) applyCatalogPlan(plan []catalog.Entry, installed []packages.Package) error {
	stage, err := os.MkdirTemp("", "bmcshell-catalog-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	files := make([]string, 0, len(plan))
	origins := make(map[string]string, len(plan))
	for _, entry := range plan {
		file, err := catalog.Download(context.Background(), entry, stage)
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
	if err := a.commitCatalogPlan(prepared); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Activated %d package changes.\n", len(plan))
	return nil
}
