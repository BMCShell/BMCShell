package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"bmcshell/internal/bookdb"
	"bmcshell/internal/packages"
	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

type app struct {
	version, home string
	store         *packages.Store
	in            io.Reader
	out, errOut   io.Writer
}

func Run(version string, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(out, "BMCShell %s\n", version)
		return 0
	}
	a := app{version: version, in: in, out: out, errOut: errOut}
	if err := a.run(args); err != nil {
		if errors.Is(err, bookdb.ErrNoMatch) {
			return 4
		}
		fmt.Fprintln(errOut, "BMCShell:", err)
		var child *exec.ExitError
		if errors.As(err, &child) && child.ExitCode() > 0 && child.ExitCode() <= 255 {
			return child.ExitCode()
		}
		return 1
	}
	return 0
}

func (a *app) run(args []string) error {
	if len(args) == 0 || len(args) == 1 && args[0] == "--help" {
		return a.help(nil)
	}
	if args[0] == "help" && (len(args) < 3 || len(args) == 3 && args[2] == "--usage") {
		return a.help(args[1:])
	}
	a.home = os.Getenv("BMCSHELL_HOME")
	if a.home == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		a.home = filepath.Join(config, "BMCShell")
	}
	var err error
	a.store, err = packages.Open(a.home)
	if err != nil {
		return err
	}
	command, rest := args[0], args[1:]
	if len(rest) == 1 && rest[0] == "--help" {
		return a.help([]string{command, "--usage"})
	}
	switch command {
	case "help":
		return a.help(rest)
	case "doctor":
		return a.doctor(rest)
	case "setting":
		return a.setting(rest)
	case "status":
		return a.status(rest)
	case "proxy":
		return a.proxyCommand(rest)
	case "plugin":
		return a.plugin(rest)
	case "book":
		return a.book(rest)
	case "library":
		return a.library(rest)
	case "tool":
		return a.tool(rest)
	case "bridge":
		return a.bridge(rest)
	case "use", "exit":
		return errors.New("load shell/bmcshell.sh in Bash and use the bmc function to change the current session")
	default:
		return fmt.Errorf("unknown command %q; see bmc help", command)
	}
}

func (a *app) doctor(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: bmc doctor")
	}
	fmt.Fprintf(a.out, "BMCShell %s\nConfig: %s\n", a.version, a.home)
	list, err := a.store.List()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Installed packages: %d\n", len(list))
	for _, p := range list {
		for _, dep := range p.SystemDependencies {
			path, err := exec.LookPath(dep.ID)
			if err != nil {
				fmt.Fprintf(a.out, "%s: %s not found; install it with the host package manager\n", p.Name, dep.ID)
				continue
			}
			fmt.Fprintf(a.out, "%s: %s at %s", p.Name, dep.ID, path)
			if len(dep.Features) > 0 {
				fmt.Fprintf(a.out, " (feature checks pending: %s)", strings.Join(dep.Features, ", "))
			}
			fmt.Fprintln(a.out)
		}
	}
	return nil
}

func (a *app) status(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: bmc status")
	}
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	if ctx == nil {
		fmt.Fprintln(a.out, "No active BMC target.")
		return nil
	}
	for _, key := range sortedKeys(ctx) {
		fmt.Fprintf(a.out, "%s = %s\n", key, ctx[key])
	}
	return nil
}

func (a *app) setting(args []string) error {
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		for _, key := range []string{"use.default_resolver", "theme.active", "proxy.address"} {
			fmt.Fprintf(a.out, "%s = %s\n", key, settingValue(cfg, key))
		}
		return nil
	}
	if len(args) == 1 && args[0] == "--check" {
		if _, err := a.environment(); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "Settings are valid.")
		return nil
	}
	if len(args) < 2 {
		return errors.New("see bmc help setting --usage")
	}
	action, key := args[0], args[1]
	if action == "--import-env" && len(args) == 2 {
		return a.importEnvironment(key, cfg)
	}
	if key != "use.default_resolver" && key != "theme.active" && key != "proxy.address" {
		return fmt.Errorf("unknown setting %q", key)
	}
	switch {
	case action == "--get" && len(args) == 2:
		fmt.Fprintln(a.out, settingValue(cfg, key))
		return nil
	case action == "--reset" && len(args) == 2:
		switch key {
		case "use.default_resolver":
			cfg.DefaultResolver = nil
		case "theme.active":
			cfg.Theme = ""
		case "proxy.address":
			cfg.Proxy = ""
		}
	case action == "--set" && len(args) == 3:
		if err := a.setValue(&cfg, key, args[2]); err != nil {
			return err
		}
	default:
		return errors.New("see bmc help setting --usage")
	}
	return settings.Save(a.home, cfg)
}

func settingValue(cfg settings.Config, key string) string {
	if key == "proxy.address" {
		if cfg.Proxy == "" {
			return "off"
		}
		return cfg.Proxy
	}
	if key == "use.default_resolver" {
		if cfg.DefaultResolver != nil {
			return cfg.DefaultResolver.Export + " (" + cfg.DefaultResolver.ExtensionID + ")"
		}
		return "generic"
	}
	if cfg.Theme == "" {
		return "none"
	}
	return cfg.Theme
}

func (a *app) setValue(cfg *settings.Config, key, value string) error {
	if key == "proxy.address" {
		address, err := settings.ProxyAddress(value)
		if err == nil {
			cfg.Proxy = address
		}
		return err
	}
	if key == "theme.active" {
		if value == "none" {
			cfg.Theme = ""
			return nil
		}
		p, err := a.findType(value, "theme")
		if err != nil {
			return err
		}
		cfg.Theme = p.ID
		return nil
	}
	list, err := a.store.List()
	if err != nil {
		return err
	}
	resolver, err := session.SelectResolver(list, value, nil)
	if err != nil {
		return err
	}
	cfg.DefaultResolver = nil
	if resolver.Name != "generic" {
		cfg.DefaultResolver = &settings.Resolver{ExtensionID: resolver.Package.ID, Export: resolver.Name}
	}
	return nil
}
