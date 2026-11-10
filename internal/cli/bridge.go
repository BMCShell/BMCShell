package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"bmcshell/internal/packages"
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
	return toolRef{}, fmt.Errorf("unknown tool %q", name)
}

func toolReady(t toolRef, ctx session.Context) error {
	if t.Spec.Scope == "session" && ctx == nil {
		return errors.New("select a BMC with bmc use first")
	}
	for _, field := range t.Spec.Requires {
		if ctx[field] == "" {
			return fmt.Errorf("session field %q is required", field)
		}
	}
	return nil
}

func (a *app) tool(args []string) error {
	if len(args) == 1 && args[0] != "list" && args[0] != "-l" {
		return a.help([]string{"tool", args[0], "--usage"})
	}
	if len(args) > 1 {
		return errors.New("usage: bmc tool [list|name]")
	}
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	list, err := a.tools()
	if err != nil {
		return err
	}
	registry, err := a.environment()
	if err != nil {
		return err
	}
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	for _, t := range list {
		status := "ready"
		err := toolReady(t, ctx)
		if err == nil && t.Spec.ManagedEnv() {
			_, err = environmentValues(t.Spec, registry, ctx, cfg.EnvDefaults)
		}
		if err != nil {
			status = err.Error()
		}
		fmt.Fprintf(a.out, "%s\t%s\t%s\n", t.Name, t.Spec.Scope, status)
	}
	return nil
}

func (a *app) bridge(args []string) error {
	if len(args) == 0 {
		return errors.New("missing shell bridge action")
	}
	switch args[0] {
	case "proxy-address":
		return a.proxyAddressBridge(args[1:])
	case "proxy-run":
		return a.proxyRunBridge(args[1:])
	case "resolve":
		return a.resolve(args[1:])
	case "tools":
		if len(args) != 2 || args[1] != "session" && args[1] != "global" {
			return errors.New("bridge tools expects global or session")
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
			return errors.New("bridge run needs a tool name")
		}
		return a.runTool(args[1], args[2:])
	case "shell-entry":
		if len(args) != 2 {
			return errors.New("bridge shell-entry needs a tool name")
		}
		t, err := a.readyTool(args[1])
		if err != nil || t.Spec.Runtime != "bash" || t.Spec.ManagedEnv() {
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
			return errors.New("bridge field needs a context field name")
		}
		switch args[1] {
		case "target.ip", "target.model", "target.resolver", "oem.sbmc.cid":
		default:
			return fmt.Errorf("unsupported public context field %q", args[1])
		}
		ctx, err := session.Current()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.out, ctx[args[1]])
		return err
	case "prompt":
		if len(args) != 1 {
			return errors.New("bridge prompt takes no arguments")
		}
		return a.prompt()
	default:
		return fmt.Errorf("unknown shell bridge action %q", args[0])
	}
}

func (a *app) resolve(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bmc use <IP> [--resolver <name>] [-- <resolver args...>]")
	}
	ip, extra := args[0], args[1:]
	var name string
	if len(extra) != 0 && extra[0] == "--resolver" {
		if len(extra) < 2 || extra[1] == "" || strings.HasPrefix(extra[1], "-") {
			return errors.New("--resolver needs a name")
		}
		name, extra = extra[1], extra[2:]
	}
	if len(extra) != 0 {
		if extra[0] != "--" {
			return errors.New("resolver arguments must follow --")
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
	ctx, err := session.Resolve(resolver, ip, extra, a.in, a.errOut)
	if err != nil {
		return err
	}
	registry, err := a.store.Environment(list)
	if err != nil {
		return err
	}
	for name, field := range registry {
		if value, ok := ctx[field.Key]; ok && (field.Sensitive || field.Validate(value) != nil) {
			return fmt.Errorf("resolver returned an invalid or sensitive value for %s", name)
		}
	}
	return json.NewEncoder(a.out).Encode(ctx)
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
		return errors.New("load shell/bmcshell.sh in Bash to run this tool")
	}
	path, err := t.Package.File(t.Spec.Entry)
	if err != nil {
		return err
	}
	executable := path
	if t.Spec.Runtime == "bash" {
		executable = os.Getenv("BMCSHELL_BRIDGE_BASH")
		if !filepath.IsAbs(executable) {
			return errors.New("load shell/bmcshell.sh in Bash to run this tool")
		}
		args = append([]string{"--", filepath.ToSlash(path)}, args...)
	}
	cmd := exec.Command(executable, args...)
	registry, err := a.environment()
	if err != nil {
		return err
	}
	cmd.Env = append(toolEnvironment(registry, t.Spec.ManagedEnv()), "BMCSHELL_ENTRY="+name)
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
			return errors.New("environment writes need an active Bash session")
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
	p, err := a.findType(cfg.Theme, "theme")
	if err != nil {
		return err
	}
	var theme struct{ Prefix string }
	if err := readPackageJSON(p, p.Data, &theme); err != nil {
		return err
	}
	if len(theme.Prefix) > 120 || !session.Printable(theme.Prefix) || strings.ContainsAny(theme.Prefix, "$`\\") {
		return errors.New("invalid theme prompt prefix")
	}
	fmt.Fprint(a.out, strings.ReplaceAll(theme.Prefix, "{ip}", ctx["target.ip"]))
	return nil
}
