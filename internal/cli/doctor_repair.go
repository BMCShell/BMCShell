package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"bmcshell/internal/packages"
	"bmcshell/internal/proxy"
)

type msys2RepairAction struct {
	dependencies map[string]bool
	sources      map[string]bool
}

func msys2RepairActions(records []packages.RepairRecord, missing map[string]bool) map[string]msys2RepairAction {
	actions := make(map[string]msys2RepairAction)
	for _, item := range records {
		if item.Manager != "msys2" || item.Platform != "windows-amd64" || !missing[item.Dependency] {
			continue
		}
		action, exists := actions[item.Package]
		if !exists {
			action = msys2RepairAction{dependencies: make(map[string]bool), sources: make(map[string]bool)}
			actions[item.Package] = action
		}
		action.dependencies[item.Dependency] = true
		action.sources[item.SourceName] = true
	}
	return actions
}

func (a *app) repairMissing(records []packages.RepairRecord, missing map[string]bool) error {
	actions := msys2RepairActions(records, missing)
	if len(actions) == 0 {
		fmt.Fprintln(a.out, "这些依赖暂无可用的自动修复方式。")
		return nil
	}
	pacman := msys2Pacman(os.Getenv("BMCSHELL_BRIDGE_BASH"), os.Getenv("PATH"))
	if pacman == "" {
		fmt.Fprintln(a.out, "自动安装需要在 MSYS2 Bash 中运行，并将其包管理器 pacman 加入 PATH。")
		return nil
	}
	if !terminalInput(a.in) {
		fmt.Fprintln(a.out, "请在交互终端中选择要修复的依赖。")
		return nil
	}
	names := sortedKeys(actions)
	for i, name := range names {
		fmt.Fprintf(a.out, "%d. %s (%s) [%s]\n", i+1, name,
			strings.Join(sortedKeys(actions[name].dependencies), ", "), strings.Join(sortedKeys(actions[name].sources), ", "))
	}
	fmt.Fprint(a.out, "请输入要安装的包编号，多个编号以空格分隔，直接回车取消：")
	line, readErr := bufio.NewReader(a.in).ReadString('\n')
	if readErr != nil && line == "" {
		return nil
	}
	selected, err := selectedRepairs(names, line)
	if err != nil || len(selected) == 0 {
		return err
	}

	address, err := a.proxyAddress()
	if err != nil {
		return err
	}
	args := append([]string{"-S", "--"}, selected...)
	cmd := exec.Command(pacman, args...)
	cmd.Env = proxy.Environment(os.Environ(), address)
	if address != "" {
		url := "socks5h://" + address
		cmd.Env = append(cmd.Env, "http_proxy="+url, "https_proxy="+url, "ALL_PROXY="+url)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.in, a.out, a.errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("MSYS2 自动修复失败：%w", err)
	}
	var unresolved []string
	for _, name := range selected {
		for _, dependency := range sortedKeys(actions[name].dependencies) {
			if _, err := exec.LookPath(dependency); err != nil {
				unresolved = append(unresolved, dependency)
			}
		}
	}
	if len(unresolved) > 0 {
		return fmt.Errorf("修复后仍找不到依赖：%s", strings.Join(unresolved, ", "))
	}
	fmt.Fprintln(a.out, "所选依赖已可用。")
	return nil
}

func selectedRepairs(names []string, line string) ([]string, error) {
	var selected []string
	seen := make(map[int]bool)
	for _, token := range strings.Fields(line) {
		index, err := strconv.Atoi(token)
		if err != nil || index < 1 || index > len(names) {
			return nil, errors.New("修复选项的编号无效")
		}
		if !seen[index] {
			selected = append(selected, names[index-1])
			seen[index] = true
		}
	}
	return selected, nil
}

func msys2Pacman(bashPath, searchPath string) string {
	if runtime.GOOS != "windows" || !filepath.IsAbs(bashPath) {
		return ""
	}
	if filepath.Ext(bashPath) == "" {
		bashPath += ".exe"
	}
	bashPath, err := filepath.EvalSymlinks(bashPath)
	if err != nil || !strings.EqualFold(filepath.Base(bashPath), "bash.exe") {
		return ""
	}
	bin := filepath.Dir(bashPath)
	root := filepath.Dir(filepath.Dir(bin))
	if !strings.EqualFold(bin, filepath.Join(root, "usr", "bin")) {
		return ""
	}
	onPath := false
	for _, candidate := range filepath.SplitList(searchPath) {
		if strings.EqualFold(filepath.Clean(candidate), bin) {
			onPath = true
			break
		}
	}
	if !onPath {
		return ""
	}
	pacman := filepath.Join(bin, "pacman.exe")
	for _, path := range []string{pacman, filepath.Join(root, "etc", "pacman.conf"), filepath.Join(root, "usr", "share", "pacman", "keyrings", "msys2.gpg")} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return ""
		}
	}
	database, err := os.Lstat(filepath.Join(root, "var", "lib", "pacman", "local"))
	if err != nil || !database.IsDir() {
		return ""
	}
	return pacman
}
