package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"bmcshell/internal/bookdb"
	"bmcshell/internal/packages"
	"bmcshell/internal/release"
	"bmcshell/internal/session"
)

type app struct {
	version, home string
	store         *packages.Store
	in            io.Reader
	out, errOut   io.Writer
}

func Run(version string, args []string, in io.Reader, out, errOut io.Writer) int {
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

func helpFlag(arg string) bool {
	return arg == "-h" || arg == "--help"
}

func (a *app) run(args []string) error {
	if len(args) == 0 || len(args) == 1 && helpFlag(args[0]) {
		return a.help(nil)
	}
	command, rest := args[0], args[1:]
	if (command == "version" || command == "--version") && len(rest) == 0 {
		fmt.Fprintf(a.out, "BMCShell %s\n", a.version)
		return nil
	}
	if len(rest) == 1 && helpFlag(rest[0]) {
		return a.help([]string{command})
	}
	if command == "help" && len(rest) <= 1 {
		return a.help(rest)
	}

	var err error
	a.home, err = dataDirectory()
	if err != nil {
		return err
	}
	a.store, err = packages.Open(a.home)
	if err != nil {
		return err
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
	case "update":
		return a.update(rest)
	case "bridge":
		return a.bridge(rest)
	case "use", "exit":
		return errors.New("load bin/bmcshell.sh from an installed release or shell/bmcshell.sh from the source tree to change the current Bash session")
	case "version":
		return errors.New("bmc version does not take arguments; see bmc version --help")
	default:
		return fmt.Errorf("unknown command %q; see bmc help", command)
	}
}

func dataDirectory() (string, error) {
	if configured := os.Getenv("BMCSHELL_HOME"); configured != "" {
		return configured, nil
	}
	if executable, err := os.Executable(); err == nil {
		if root, ok := release.PortableRoot(executable); ok {
			return root, nil
		}
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "BMCShell"), nil
}

func (a *app) status(args []string) error {
	showEnv := len(args) == 1 && args[0] == "--env"
	if len(args) != 0 && !showEnv {
		return errors.New("usage: bmc status [--env]")
	}
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	if ctx == nil {
		fmt.Fprintln(a.out, "No active BMC target.")
		return nil
	}
	if showEnv {
		return a.statusEnvironment(ctx)
	}
	fmt.Fprintln(a.out, ctx["target.ip"])
	return nil
}
