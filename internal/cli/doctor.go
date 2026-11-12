package cli

import (
	_ "embed"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"bmcshell/internal/packages"
)

//go:embed doctor-repairs.json
var frameworkRepairList []byte

func collectRepairSuggestions(installed []packages.Package) ([]packages.RepairRecord, error) {
	records, err := packages.ParseRepairList(packages.RepairFrameworkSource, frameworkRepairList)
	if err != nil {
		return nil, fmt.Errorf("无法读取框架的修复建议：%w", err)
	}
	for _, p := range installed {
		pluginRecords, err := p.RepairRecords()
		if err != nil {
			return nil, err
		}
		records = append(records, pluginRecords...)
	}
	return records, nil
}

func (a *app) doctor(args []string) error {
	var detailed, auto bool
	var filter string
	for len(args) > 0 {
		switch args[0] {
		case "-d", "--detail":
			detailed = true
		case "-a", "--auto":
			auto = true
		case "-f", "--filter":
			if len(args) < 2 || args[1] != "ok" && args[1] != "missing" {
				return fmt.Errorf("--filter 只接受 ok（正常）或 missing（缺失和损坏）。运行 bmc doctor --help 查看用法")
			}
			filter = args[1]
			args = args[1:]
		default:
			return fmt.Errorf("未知的诊断选项 %q。运行 bmc doctor --help 查看用法", args[0])
		}
		args = args[1:]
	}
	if detailed {
		fmt.Fprintf(a.out, "  版本：%s\n  数据目录：%s\n", a.version, a.home)
	}
	installed, damaged, index, err := a.store.SyncRepairSuggestions(collectRepairSuggestions)
	if err != nil {
		return fmt.Errorf("无法检查扩展包：%w", err)
	}
	if len(damaged) == 0 {
		if filter == "" || filter == "ok" {
			fmt.Fprintln(a.out, "BMCShell：正常")
		}
	} else if filter != "ok" {
		for _, issue := range damaged {
			fmt.Fprintf(a.out, "损坏的扩展包：%s\n", issue.Error())
		}
		fmt.Fprintln(a.out, "使用 bmc plugin install --file 重新安装原发布包，或使用 bmc plugin remove 按 UUID 卸载。")
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	byDependency := make(map[string][]packages.RepairRecord)
	for _, item := range index {
		if item.Platform == "" || item.Platform == platform {
			byDependency[item.Dependency] = append(byDependency[item.Dependency], item)
		}
	}

	missing := make(map[string]bool)
	for _, p := range installed {
		for _, dep := range p.SystemDependencies {
			path, err := exec.LookPath(dep.ID)
			if filter == "ok" && err != nil || filter == "missing" && err == nil {
				continue
			}
			status := "正常"
			if err != nil {
				status = "缺失"
			}
			fmt.Fprintf(a.out, "%s/%s：%s\n", p.Name, dep.ID, status)
			if !detailed && !auto {
				continue
			}
			if err != nil {
				missing[dep.ID] = true
				a.printRepairSuggestions(p, byDependency[dep.ID])
			} else {
				fmt.Fprintf(a.out, "  路径：%s\n", path)
			}
			if dep.MinVersion != "" {
				fmt.Fprintf(a.out, "  要求版本：%s\n", dep.MinVersion)
			}
			if len(dep.Features) != 0 {
				fmt.Fprintf(a.out, "  要求功能：%s\n", strings.Join(dep.Features, ", "))
			}
		}
	}
	if auto && len(missing) != 0 {
		return a.repairMissing(index, missing)
	}
	return nil
}

func (a *app) printRepairSuggestions(p packages.Package, records []packages.RepairRecord) {
	shown := false
	for _, item := range records {
		if item.SourceID != packages.RepairFrameworkSource && item.SourceID != p.ID {
			continue
		}
		shown = true
		fmt.Fprintf(a.out, "  [%s] %s\n", item.SourceName, item.Suggestion)
		if item.Manager != "" {
			fmt.Fprintf(a.out, "    %s：%s\n", item.Manager, item.Package)
		}
	}
	if !shown {
		fmt.Fprintln(a.out, "  请通过当前终端或系统的包管理器安装此依赖。")
	}
}
