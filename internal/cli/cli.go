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
		var child *exec.ExitError
		if errors.As(err, &child) {
			code := child.ExitCode()
			status := "子进程异常终止"
			if code >= 0 {
				status = fmt.Sprintf("子进程退出码 %d", code)
			}
			message := strings.Replace(err.Error(), child.Error(), status, 1)
			fmt.Fprintf(errOut, "BMCShell：%s\n", message)
			if code > 0 && code <= 255 {
				return code
			}
			return 1
		}
		fmt.Fprintf(errOut, "BMCShell：%v\n", err)
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
	if command == "help" {
		if _, builtin := builtinHelp[strings.Join(rest, " ")]; len(rest) <= 1 || builtin {
			return a.help(rest)
		}
	}
	if command == "plugin" && len(rest) == 2 && helpFlag(rest[1]) {
		if rest[0] == "help" {
			return a.help([]string{"plugin"})
		}
		return a.help([]string{"plugin", rest[0]})
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
		return errors.New("请先在当前 Bash 中加载安装目录的 bin/bmcshell.sh 或源码目录的 shell/bmcshell.sh，再运行此命令")
	case "version":
		return errors.New("bmc version 不接受参数。运行 bmc version --help 查看用法")
	default:
		return fmt.Errorf("未知命令 %q。运行 bmc help 查看可用命令", command)
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
		return errors.New("bmc status 参数无效。运行 bmc status --help 查看用法")
	}
	ctx, err := session.Current()
	if err != nil {
		return err
	}
	if showEnv {
		return a.statusEnvironment(ctx)
	}
	if ctx == nil {
		fmt.Fprintln(a.out, "尚未选中 BMC 目标。请先运行 bmc use <IP>。")
		return nil
	}
	fmt.Fprintln(a.out, ctx["target.ip"])
	return a.statusResolver(ctx)
}
