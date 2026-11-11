package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"bmcshell/internal/packages"
	"bmcshell/internal/proxy"
	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

type toolRef struct {
	Name    string
	Spec    packages.Tool
	Package packages.Package
}

func (a *app) tools() ([]toolRef, error) {
	list, err := a.store.List()
	if err != nil {
		return nil, err
	}
	var tools []toolRef
	for _, p := range list {
		for name, spec := range p.Provides.Tools {
			tools = append(tools, toolRef{Name: name, Spec: spec, Package: p})
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}

func (a *app) findTool(name string) (toolRef, error) {
	list, err := a.tools()
	if err != nil {
		return toolRef{}, err
	}
	for _, t := range list {
		if t.Name == name {
			return t, nil
		}
	}
	return toolRef{}, fmt.Errorf("未找到工具 %q。运行 bmc tool --all 查看所有工具", name)
}

func toolReady(t toolRef, ctx session.Context) error {
	if t.Spec.Scope == "session" && ctx == nil {
		return errors.New("尚未选中 BMC 目标。请先运行 bmc use <IP>")
	}
	for _, field := range t.Spec.Requires {
		if ctx[field] == "" {
			return fmt.Errorf("缺少会话字段 %q", field)
		}
	}
	return nil
}

func toolAvailability(p packages.Package, name string) error {
	for _, dep := range p.SystemDependencies {
		if len(dep.RequiredBy) != 0 && !slices.Contains(dep.RequiredBy, name) {
			continue
		}
		if _, err := exec.LookPath(dep.ID); err != nil {
			return fmt.Errorf("缺少依赖 %s", dep.ID)
		}
	}
	return nil
}

func (a *app) tool(args []string) error {
	const usage = "工具查询参数无效。运行 bmc tool --help 查看用法"
	if len(args) == 2 && (args[1] == "-h" || args[1] == "--help") {
		if resolver, ok := strings.CutPrefix(args[0], "use@"); ok {
			return a.help([]string{"use", resolver})
		}
		return a.help([]string{"tool", args[0]})
	}
	if len(args) > 1 {
		return errors.New(usage)
	}
	all, selected := false, ""
	if len(args) == 1 {
		switch args[0] {
		case "-l", "--list":
		case "-a", "--all":
			all = true
		default:
			selected = args[0]
			if strings.HasPrefix(selected, "-") {
				return errors.New(usage)
			}
		}
	}
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	list, err := a.store.List()
	if err != nil {
		return err
	}
	var selectedResolver session.Resolver
	if selector, ok := strings.CutPrefix(selected, "use@"); ok && selector != "" && selector != "generic" {
		selectedResolver, err = session.SelectResolver(list, selector, nil)
		if err != nil {
			return err
		}
		selected = "use@" + selectedResolver.Selector()
	}
	registry, err := a.store.Environment(list)
	if err != nil {
		return err
	}
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	type entry struct {
		name, scope string
		err         error
	}
	groups := make(map[string][]entry)
	for _, p := range list {
		for _, name := range sortedKeys(p.Provides.Tools) {
			spec := p.Provides.Tools[name]
			err := toolAvailability(p, name)
			if err == nil {
				err = toolReady(toolRef{Name: name, Spec: spec, Package: p}, ctx)
			}
			if err == nil && spec.ManagedEnv() {
				_, err = environmentValuesFrom(spec, registry, ctx, cfg.EnvDefaults, shellStatusValues)
			}
			groups[p.Name] = append(groups[p.Name], entry{name, spec.Scope, err})
		}
		for _, name := range sortedKeys(p.Provides.Resolvers) {
			if selectedResolver.Name != "" && (p.ID != selectedResolver.Package.ID || name != selectedResolver.Name) {
				continue
			}
			resolver := session.Resolver{Name: name, Package: p}
			groups[p.Name] = append(groups[p.Name], entry{"use@" + resolver.Selector(), "resolver", toolAvailability(p, name)})
		}
	}
	for _, group := range sortedKeys(groups) {
		shown := false
		for _, item := range groups[group] {
			if selected != "" && item.name != selected || selected == "" && !all && item.err != nil {
				continue
			}
			if !shown {
				fmt.Fprintln(a.out, group)
				shown = true
			}
			status := "可用"
			if item.err != nil {
				status = item.err.Error()
			}
			fmt.Fprintf(a.out, "  %s\t%s\t%s\n", item.name, toolScopeLabel(item.scope), status)
			if selected != "" {
				return nil
			}
		}
	}
	if selected != "" {
		return fmt.Errorf("未找到工具 %q。运行 bmc tool --all 查看所有工具，或用 bmc tool --help 查看用法", selected)
	}
	return nil
}

func toolScopeLabel(scope string) string {
	switch scope {
	case "global":
		return "全局工具"
	case "session":
		return "会话工具"
	case "resolver":
		return "解析器"
	default:
		return scope
	}
}

func (a *app) completion(args []string) error {
	if len(args) != 1 {
		return errors.New("未指定命令补全类别")
	}
	list, err := a.store.List()
	if err != nil {
		return err
	}
	values := make(map[string]bool)
	for _, p := range list {
		switch args[0] {
		case "tools":
			for name := range p.Provides.Tools {
				values[name] = true
			}
			for name := range p.Provides.Resolvers {
				resolver := session.Resolver{Name: name, Package: p}
				values["use@"+resolver.Selector()] = true
			}
		case "books":
			if p.Type == "book" {
				values[p.DataName()] = true
			}
		case "resolvers":
			for name := range p.Provides.Resolvers {
				resolver := session.Resolver{Name: name, Package: p}
				values[resolver.Selector()] = true
			}
		}
	}
	if args[0] == "resolvers" {
		values["generic"] = true
	}
	for _, value := range sortedKeys(values) {
		fmt.Fprintln(a.out, value)
	}
	return nil
}

func (a *app) bridge(args []string) error {
	if len(args) == 0 {
		return errors.New("未指定 Bash 适配操作")
	}
	switch args[0] {
	case "history":
		return a.history(args[1:])
	case "proxy-address":
		return a.proxyAddressBridge(args[1:])
	case "proxy-config":
		return a.proxyConfigBridge(args[1:])
	case "completion":
		return a.completion(args[1:])
	case "proxy-run":
		return a.proxyRunBridge(args[1:])
	case "resolve":
		return a.resolve(args[1:])
	case "tools":
		if len(args) != 2 || args[1] != "session" && args[1] != "global" {
			return errors.New("bridge tools 只接受 global 或 session")
		}
		list, err := a.tools()
		if err != nil {
			return err
		}
		for _, t := range list {
			if t.Spec.Scope == args[1] {
				fmt.Fprintln(a.out, t.Name)
			}
		}
		return nil
	case "run":
		if len(args) < 2 {
			return errors.New("bridge run 需要指定工具名")
		}
		return a.runTool(args[1], args[2:])
	case "shell-entry":
		if len(args) != 2 {
			return errors.New("bridge shell-entry 需要指定工具名")
		}
		t, err := a.readyTool(args[1])
		if err != nil || t.Spec.Runtime != "bash" || t.Spec.ManagedEnv() {
			return err
		}
		address, err := a.proxyAddress()
		if err != nil {
			return err
		}
		if err := proxy.CheckCapabilities(address, t.Spec.ProxyCapabilities); err != nil {
			return err
		}
		path, err := t.Package.File(t.Spec.Entry)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.out, path)
		return err
	case "field":
		if len(args) != 2 {
			return errors.New("bridge field 需要指定会话字段名")
		}
		switch args[1] {
		case "target.ip", "target.model", "target.resolver", "oem.sbmc.cid", "snmp.enterprises":
		default:
			return fmt.Errorf("不能通过此命令读取会话字段 %q", args[1])
		}
		ctx, err := session.Current()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.out, ctx[args[1]])
		return err
	case "prompt":
		if len(args) != 1 {
			return errors.New("bridge prompt 不接受参数")
		}
		return a.prompt()
	default:
		return fmt.Errorf("未知的 Bash 适配操作 %q", args[0])
	}
}

func (a *app) resolve(args []string) error {
	if len(args) == 0 {
		return errors.New("请指定目标 IP。运行 bmc use --help 查看用法")
	}
	ip, extra := args[0], args[1:]
	var name string
	if len(extra) != 0 && extra[0] == "--resolver" {
		if len(extra) < 2 || extra[1] == "" || strings.HasPrefix(extra[1], "-") {
			return errors.New("--resolver 需要指定解析器包名、导出名或 generic。运行 bmc use --help 查看用法")
		}
		name, extra = extra[1], extra[2:]
	}
	if len(extra) != 0 {
		if extra[0] != "--" {
			return errors.New("解析器参数必须放在 -- 后。运行 bmc use --help 查看用法")
		}
		extra = extra[1:]
	}
	var fallback *settings.Resolver
	if name == "" {
		cfg, err := settings.Load(a.home)
		if err != nil {
			return err
		}
		fallback = cfg.DefaultResolver
	}
	list, err := a.store.List()
	if err != nil {
		return err
	}
	resolver, err := session.SelectResolver(list, name, fallback)
	if err != nil {
		return err
	}
	var address string
	if resolver.Name != "generic" {
		address, err = a.proxyAddress()
		if err != nil {
			return err
		}
	}
	registry, err := a.store.Environment(list)
	if err != nil {
		return err
	}
	environment, err := a.resolverEnvironment(resolver.Package, list, registry)
	if err != nil {
		return err
	}
	result, err := session.Resolve(resolver, ip, extra, address, environment, a.in, a.errOut)
	if err != nil {
		return err
	}
	for name, field := range registry {
		if value, ok := result.Context[field.Key]; ok && (field.Sensitive || field.Validate(value) != nil) {
			return fmt.Errorf("解析器为 %s 返回了无效值或敏感值", name)
		}
	}
	if err := json.NewEncoder(a.out).Encode(result.Context); err != nil {
		return err
	}
	// Only the adapter requests and captures the private lines.
	if result.Password != "" && os.Getenv("BMCSHELL_BRIDGE_RESOLVE_PRIVATE") == "1" {
		_, err = fmt.Fprintln(a.out, result.Username+"\n"+result.Password)
	}
	return err
}

func (a *app) readyTool(name string) (toolRef, error) {
	t, err := a.findTool(name)
	if err != nil {
		return toolRef{}, err
	}
	ctx, err := session.Current()
	if err != nil {
		return toolRef{}, err
	}
	return t, toolReady(t, ctx)
}

func (a *app) runTool(name string, args []string) error {
	t, err := a.readyTool(name)
	if err != nil {
		return err
	}
	if t.Spec.Runtime == "bash" && !t.Spec.ManagedEnv() {
		return errors.New("请先在当前 Bash 中加载 bmcshell.sh，再运行此工具")
	}
	path, err := t.Package.File(t.Spec.Entry)
	if err != nil {
		return err
	}
	executable := path
	if t.Spec.Runtime == "bash" {
		executable = os.Getenv("BMCSHELL_BRIDGE_BASH")
		if !filepath.IsAbs(executable) {
			return errors.New("请先在当前 Bash 中加载 bmcshell.sh，再运行此工具")
		}
		args = append([]string{"--", filepath.ToSlash(path)}, args...)
	}
	cmd := exec.Command(executable, args...)
	registry, err := a.environment()
	if err != nil {
		return err
	}
	address, err := a.proxyAddress()
	if err != nil {
		return err
	}
	if err := proxy.CheckCapabilities(address, t.Spec.ProxyCapabilities); err != nil {
		return err
	}
	cmd.Env = append(proxy.Environment(toolEnvironment(registry, t.Spec.ManagedEnv()), address), "BMCSHELL_ENTRY="+name)
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	if t.Spec.ManagedEnv() {
		cfg, err := settings.Load(a.home)
		if err != nil {
			return err
		}
		values, err := environmentValues(t.Spec, registry, ctx, cfg.EnvDefaults)
		if err != nil {
			return err
		}
		for _, key := range sortedKeys(values) {
			cmd.Env = append(cmd.Env, key+"="+values[key])
		}
	}
	var resultPath string
	destination := os.Getenv("BMCSHELL_BRIDGE_RESULT_FILE")
	if len(t.Spec.EnvWrites) != 0 {
		if ctx == nil || !filepath.IsAbs(destination) {
			return errors.New("写入环境变量需要已启用的 Bash 会话")
		}
		result, err := os.CreateTemp("", "bmcshell-tool-*.json")
		if err != nil {
			return err
		}
		resultPath = result.Name()
		defer os.Remove(resultPath)
		if err := result.Close(); err != nil {
			return err
		}
	}
	cmd.Env = append(cmd.Env, "BMCSHELL_RESULT_FILE="+resultPath)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.in, a.out, a.errOut
	if err := cmd.Run(); err != nil {
		return err
	}
	if resultPath != "" {
		return applyToolWrites(resultPath, destination, t.Spec, registry, ctx)
	}
	return nil
}

func (a *app) prompt() error {
	ctx, err := session.Current()
	if err != nil || ctx == nil {
		return err
	}
	cfg, err := settings.Load(a.home)
	if err != nil || cfg.Theme == "" {
		return err
	}
	p, err := a.store.Find(cfg.Theme)
	if err != nil {
		return err
	}
	if p.Type != "theme" {
		return errors.New("当前主题设置引用的包不是主题包")
	}
	var theme struct{ Prefix string }
	if err := readPackageJSON(p, p.Data, &theme); err != nil {
		return err
	}
	if len(theme.Prefix) > 120 || !session.Printable(theme.Prefix) || strings.ContainsAny(theme.Prefix, "$`\\") {
		return errors.New("主题设置的提示符前缀无效")
	}
	fmt.Fprint(a.out, strings.ReplaceAll(theme.Prefix, "{ip}", ctx["target.ip"]))
	return nil
}
