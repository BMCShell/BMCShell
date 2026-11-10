package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"bmcshell/internal/bookdb"
	"bmcshell/internal/packages"
	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

func (a *app) plugin(args []string) error {
	if len(args) == 0 || len(args) == 1 && args[0] == "list" {
		return a.listPackages("")
	}
	switch args[0] {
	case "source":
		return a.pluginSource(args[1:])
	case "update":
		return a.pluginUpdate(args[1:])
	case "install":
		if len(args) < 2 || args[1] != "--file" {
			return a.pluginFromCatalog(args[1:], false)
		}
		if len(args) != 3 {
			return errors.New("usage: bmc plugin install --file <directory-or-zip>")
		}
		p, err := a.store.Install(args[2])
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Installed %s %s (%s)\n", p.Name, p.Version, p.ID)
		return nil
	case "upgrade":
		return a.pluginFromCatalog(args[1:], true)
	case "show":
		if len(args) != 2 && (len(args) != 3 || args[2] != "--changelog") {
			return errors.New("usage: bmc plugin show <ID-or-name> [--changelog]")
		}
		p, err := a.choosePackage(args[1])
		if err != nil {
			return err
		}
		if len(args) == 3 {
			return a.changelog(p)
		}
		fmt.Fprintf(a.out, "%s\n%s\n\nID: %s\nType: %s\nVersion: %s\nPlatforms: %s\n", p.Title(), p.Details(), p.ID, p.Type, p.Version, strings.Join(p.Platforms, ", "))
		if p.ShortName != "" {
			fmt.Fprintf(a.out, "Name: %s\nPublisher: %s\nShort name: %s\n", p.Name, p.Publisher, p.ShortName)
		}
		if p.Source != "" {
			fmt.Fprintf(a.out, "Source: %s\n", p.Source)
		}
		for _, id := range sortedKeys(p.Dependencies) {
			fmt.Fprintf(a.out, "Dependency: %s %s\n", id, p.Dependencies[id])
		}
		for _, id := range p.Members {
			fmt.Fprintf(a.out, "Member: %s\n", id)
		}
		return nil
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: bmc plugin remove <ID-or-name>")
		}
		p, err := a.choosePackage(args[1])
		if err != nil {
			return err
		}
		cfg, err := settings.Load(a.home)
		if err != nil {
			return err
		}
		if cfg.Theme == p.ID || cfg.DefaultResolver != nil && cfg.DefaultResolver.ExtensionID == p.ID {
			return errors.New("reset the selected theme or default resolver before removing this package")
		}
		if err := a.store.Remove(p.ID); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Removed %s (%s)\n", p.Name, p.ID)
		return nil
	default:
		return fmt.Errorf("unknown plugin action %q; see bmc help plugin --usage", args[0])
	}
}

func (a *app) choosePackage(selector string) (packages.Package, error) {
	return a.choosePackageFrom(bufio.NewReader(a.in), selector, terminalInput(a.in))
}

func (a *app) choosePackageFrom(input *bufio.Reader, selector string, choose bool) (packages.Package, error) {
	p, err := a.store.Find(selector)
	var ambiguous *packages.AmbiguousNameError
	if !errors.As(err, &ambiguous) || !choose {
		return p, err
	}
	for i, match := range ambiguous.Matches {
		source := match.Source
		if source == "" {
			source = "local"
		}
		fmt.Fprintf(a.errOut, "%d. %s (%s, %s, %s, %s)\n", i+1, match.Title(), match.Type, source, match.Version, match.ID)
	}
	fmt.Fprint(a.errOut, "Select a package number (empty to cancel): ")
	line, _ := input.ReadString('\n')
	choice, parseErr := strconv.Atoi(strings.TrimSpace(line))
	if parseErr != nil || choice < 1 || choice > len(ambiguous.Matches) {
		return p, errors.New("package selection canceled or invalid")
	}
	return ambiguous.Matches[choice-1], nil
}

func terminalInput(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (a *app) listPackages(kind string) error {
	list, err := a.store.List()
	if err != nil {
		return err
	}
	for _, p := range list {
		if kind == "" || p.Type == kind {
			fmt.Fprintf(a.out, "%s\t%s\t%s\t%s\n", p.Type, p.Name, p.Version, p.ID)
		}
	}
	return nil
}

func (a *app) findType(selector, kind string) (packages.Package, error) {
	p, err := a.store.Find(selector)
	if err == nil && p.Type != kind {
		err = fmt.Errorf("%q is a %s package, expected %s", selector, p.Type, kind)
	}
	return p, err
}

func (a *app) book(args []string) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "list" || args[0] == "-l") {
		return a.listPackages("book")
	}
	p, err := a.findType(args[0], "book")
	if err != nil {
		return err
	}
	path, err := p.File(p.Data)
	if err != nil {
		return err
	}
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	return bookdb.Query(a.out, path, args[1:], ctx["oem.sbmc.cid"])
}

func (a *app) library(args []string) error {
	if len(args) == 0 || len(args) == 1 && args[0] == "list" {
		return a.listPackages("library")
	}
	if args[0] == "refresh" {
		return a.libraryRefresh(args[1:])
	}
	if args[0] == "source" {
		return a.librarySource(args[1:])
	}
	if args[0] == "rollback" {
		return a.libraryRollback(args[1:])
	}
	if len(args) < 3 || args[0] != "query" {
		return errors.New("usage: bmc library query <ID-or-name> <key...>")
	}
	p, err := a.findType(args[1], "library")
	if err != nil {
		return err
	}
	if p.Library != nil {
		return a.libraryQuery(p, args[2:])
	}
	if len(args) != 3 {
		return errors.New("legacy Library query expects exactly one key")
	}
	var entries []struct{ Key, Value string }
	if err := readPackageJSON(p, p.Data, &entries); err != nil {
		return err
	}
	var value *string
	for _, entry := range entries {
		if entry.Key == args[2] {
			if value != nil {
				return fmt.Errorf("duplicate library key %q", args[2])
			}
			found := entry.Value
			value = &found
		}
	}
	if value == nil {
		return fmt.Errorf("library key %q not found", args[2])
	}
	fmt.Fprintln(a.out, *value)
	return nil
}

func readPackageJSON(p packages.Package, rel string, v any) error {
	path, err := p.File(rel)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
