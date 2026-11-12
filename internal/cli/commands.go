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
		return errors.New("请指定扩展包操作。运行 bmc plugin --help 查看可用操作")
	}
	action, args := args[0], args[1:]
	switch action {
	case "list":
		return a.listPlugins(args)
	case "help":
		if len(args) == 0 {
			return a.help([]string{"plugin"})
		}
	case "update":
		if len(args) == 0 {
			return a.pluginUpdate()
		}
	case "search":
		return a.searchPlugins(args)
	case "install":
		if len(args) > 1 && args[0] == "--file" {
			return a.installLocalPackages(args[1:], bufio.NewReader(a.in), terminalInput(a.in))
		}
		if len(args) > 0 {
			for _, selector := range args {
				if strings.HasPrefix(selector, "-") {
					return errors.New("扩展包安装参数无效。运行 bmc plugin install --help 查看用法")
				}
			}
			return a.pluginFromCatalog(false, args...)
		}
	case "upgrade":
		if len(args) == 0 {
			return a.pluginFromCatalog(true)
		}
		if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
			return a.pluginFromCatalog(true, args[0])
		}
	case "remove":
		if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
			return a.removePackage(args[0])
		}
	}
	if _, ok := builtinHelp["plugin "+action]; ok {
		return fmt.Errorf("扩展包操作 %q 的参数缺失或无效。运行 bmc plugin %s --help 查看用法", action, action)
	}
	return fmt.Errorf("未知扩展包操作 %q。运行 bmc plugin --help 查看可用操作", action)
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
		return errors.New("此包是当前主题或默认解析器。请先通过 bmc setting 更改对应设置，再卸载此包")
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
	fmt.Fprintf(a.out, "已卸载 %s\n", label)
	return nil
}

func (a *app) choosePackageFrom(input *bufio.Reader, name string, interactive bool) (packages.Package, error) {
	p, err := a.store.FindInstalled(name)
	var ambiguous *packages.AmbiguousNameError
	if !errors.As(err, &ambiguous) {
		return p, err
	}
	if !interactive {
		return p, fmt.Errorf("包名 %q 匹配多个已安装的包。请在交互终端中选择，或指定包的 UUID", name)
	}
	for i, match := range ambiguous.Matches {
		source := match.Source
		if source == "" {
			source = "本地"
		}
		fmt.Fprintf(a.errOut, "%d. %s (%s, %s, %s)\n", i+1, match.Title(), match.Type, source, match.Version)
	}
	fmt.Fprint(a.errOut, "请输入包编号，直接回车取消：")
	line, _ := input.ReadString('\n')
	choice, parseErr := strconv.Atoi(strings.TrimSpace(line))
	if parseErr != nil || choice < 1 || choice > len(ambiguous.Matches) {
		return p, errors.New("包选择已取消或编号无效")
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

func (a *app) listPlugins(args []string) error {
	var kind string
	var installedOnly, detail bool
	for _, arg := range args {
		switch {
		case !installedOnly && (arg == "-i" || arg == "--installed"):
			installedOnly = true
		case !detail && pluginDetailFlag(arg):
			detail = true
		case kind == "" && packages.ValidType(arg):
			kind = arg
		default:
			return errors.New("扩展包列表参数无效。运行 bmc plugin list --help 查看用法")
		}
	}
	return a.listPluginInventory(kind, installedOnly, detail)
}

func (a *app) listDataPackages(kind string) error {
	list, _, err := a.store.Inventory()
	if err != nil {
		return err
	}
	for _, p := range list {
		packageType := packages.CanonicalType(p.Type)
		if packageType == kind {
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", packageType, p.DataName(), p.Version)
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
		return p, fmt.Errorf("名称 %q 匹配多个 %s 包，请使用完整包名或 UUID：%s", name, kind, strings.Join(names, "、"))
	}
	return p, err
}

func (a *app) book(args []string) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "-l" || args[0] == "--list") {
		return a.listDataPackages("book")
	}
	p, err := a.findType(args[0], "book")
	if err != nil {
		return err
	}
	bookArgs := args[1:]
	showHelp := len(bookArgs) == 1 && helpFlag(bookArgs[0])
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
	if showHelp {
		name := p.DataName()
		if p.ShortName == "" {
			// Legacy names can contain spaces and Bash metacharacters.
			name = "'" + strings.ReplaceAll(name, "'", "'\"'\"'") + "'"
		}
		return bookdb.QueryWithEnvironment(a.out, path, bookArgs, "", nil, nil, bookdb.Display{Command: "bmc book " + name})
	}
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	registry, values, err := a.bookQueryEnvironment(ctx)
	if err != nil {
		return err
	}
	query := func(out io.Writer, display bookdb.Display) error {
		return bookdb.QueryWithEnvironment(out, path, bookArgs, ctx["oem.sbmc.cid"], registry, values, display)
	}
	return a.displayBook(query)
}

func (a *app) library(args []string) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "-l" || args[0] == "--list") {
		return a.listDataPackages("library")
	}
	if len(args) == 2 && helpFlag(args[1]) {
		return a.help([]string{"library", args[0]})
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("业务库参数无效。运行 bmc library --help 查看用法")
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
		return fmt.Errorf("无法解析 %s：%w", rel, err)
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
