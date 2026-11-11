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
	if len(args) == 0 {
		return errors.New("select a plugin action; see bmc plugin --help")
	}
	action, args := args[0], args[1:]
	switch action {
	case "list":
		if len(args) == 0 {
			return a.listPackages("", false)
		}
		if len(args) == 1 && packages.ValidType(args[0]) {
			return a.listPackages(args[0], false)
		}
	case "help":
		if len(args) == 0 || len(args) == 1 && helpFlag(args[0]) {
			return a.help([]string{"plugin"})
		}
	case "update":
		if len(args) == 0 {
			return a.pluginUpdate()
		}
	case "search":
		if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
			return a.pluginSearch(args[0])
		}
	case "install":
		if len(args) == 2 && args[0] == "--file" {
			return a.installLocalPackage(args[1], bufio.NewReader(a.in), terminalInput(a.in))
		}
		if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
			return a.pluginFromCatalog(args[0], false)
		}
	case "upgrade":
		if len(args) == 0 {
			return a.pluginFromCatalog("", true)
		}
		if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
			return a.pluginFromCatalog(args[0], true)
		}
	case "remove":
		if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
			return a.removePackage(args[0])
		}
	}
	return fmt.Errorf("unknown or incomplete plugin action %q; see bmc plugin --help", action)
}

func (a *app) removePackage(name string) error {
	p, err := a.choosePackageFrom(bufio.NewReader(a.in), name, terminalInput(a.in))
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
	if packages.CanonicalType(p.Type) == "library" {
		unlock, err := a.lockLibrary(p.ID)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if err := a.store.Remove(p.ID); err != nil {
		return err
	}
	label := p.Name
	if label == "" {
		label = p.ID
	}
	fmt.Fprintf(a.out, "Removed %s\n", label)
	return nil
}

func (a *app) choosePackageFrom(input *bufio.Reader, name string, interactive bool) (packages.Package, error) {
	p, err := a.store.FindInstalled(name)
	var ambiguous *packages.AmbiguousNameError
	if !errors.As(err, &ambiguous) {
		return p, err
	}
	if !interactive {
		return p, fmt.Errorf("ambiguous package name %q; select the package in an interactive terminal", name)
	}
	for i, match := range ambiguous.Matches {
		source := match.Source
		if source == "" {
			source = "local"
		}
		fmt.Fprintf(a.errOut, "%d. %s (%s, %s, %s)\n", i+1, match.Title(), match.Type, source, match.Version)
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
	if mode, ok := os.LookupEnv("BMCSHELL_BRIDGE_INTERACTIVE"); ok && file == os.Stdin {
		return mode == "1"
	}
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (a *app) listPackages(kind string, dataNames bool) error {
	list, damaged, err := a.store.Inventory()
	if err != nil {
		return err
	}
	for _, p := range list {
		packageType := packages.CanonicalType(p.Type)
		if kind == "" || packageType == kind {
			name := p.Name
			if dataNames {
				name = p.DataName()
			}
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", packageType, name, p.Version)
		}
	}
	if !dataNames {
		for _, issue := range damaged {
			if kind == "" || issue.Package.Type == kind {
				fmt.Fprintf(a.out, "damaged\t%s\n", issue.Error())
			}
		}
	}
	return nil
}

func (a *app) findType(name, kind string) (packages.Package, error) {
	p, err := a.store.FindName(name, kind)
	var ambiguous *packages.AmbiguousNameError
	if errors.As(err, &ambiguous) {
		names := make([]string, 0, len(ambiguous.Matches))
		for _, match := range ambiguous.Matches {
			names = append(names, fmt.Sprintf("%s (%s)", match.Name, match.ID))
		}
		return p, fmt.Errorf("ambiguous %s name %q: %s", kind, name, strings.Join(names, ", "))
	}
	return p, err
}

func (a *app) book(args []string) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "-l" || args[0] == "--list") {
		return a.listPackages("book", true)
	}
	p, err := a.findType(args[0], "book")
	if err != nil {
		return err
	}
	bookArgs := args[1:]
	if len(bookArgs) == 1 {
		switch bookArgs[0] {
		case "-v", "--version":
			fmt.Fprintln(a.out, p.Version)
			return nil
		case "-h", "--help":
			if found, err := a.packageHelp(p, "package"); found || err != nil {
				return err
			}
		}
	}
	path, err := p.File(p.Data)
	if err != nil {
		return err
	}
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	registry, values, err := a.bookQueryEnvironment(ctx)
	if err != nil {
		return err
	}
	return bookdb.QueryWithEnvironment(a.out, path, bookArgs, ctx["oem.sbmc.cid"], registry, values)
}

func (a *app) library(args []string) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "-l" || args[0] == "--list") {
		return a.listPackages("library", true)
	}
	if len(args) != 1 {
		return errors.New("see bmc library --help")
	}
	p, err := a.findType(args[0], "library")
	if err != nil {
		return err
	}
	return a.libraryInfo(p)
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
