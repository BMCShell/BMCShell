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
		return nil, fmt.Errorf("framework repair suggestions: %w", err)
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
		case "-d", "--detial":
			detailed = true
		case "-a", "--auto":
			auto = true
		case "-f", "--filter":
			if len(args) < 2 || args[1] != "ok" && args[1] != "missing" {
				return fmt.Errorf("doctor filter must be ok or missing")
			}
			filter = args[1]
			args = args[1:]
		default:
			return fmt.Errorf("unknown doctor option %q; see bmc doctor --help", args[0])
		}
		args = args[1:]
	}
	if detailed {
		fmt.Fprintf(a.out, "  Version: %s\n  Data: %s\n", a.version, a.home)
	}
	installed, damaged, index, err := a.store.SyncRepairSuggestions(collectRepairSuggestions)
	if err != nil {
		return fmt.Errorf("package diagnostics: %w", err)
	}
	if len(damaged) == 0 {
		if filter == "" || filter == "ok" {
			fmt.Fprintln(a.out, "BMCShell: ok")
		}
	} else if filter != "ok" {
		for _, issue := range damaged {
			fmt.Fprintf(a.out, "Damaged package: %s\n", issue.Error())
		}
		fmt.Fprintln(a.out, "Restore the registered release with bmc plugin install --file, or remove it by UUID with bmc plugin remove.")
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
			status := "ok"
			if err != nil {
				status = "missing"
			}
			if filter != "" && filter != status {
				continue
			}
			fmt.Fprintf(a.out, "%s/%s: %s\n", p.Name, dep.ID, status)
			if !detailed && !auto {
				continue
			}
			if err != nil {
				missing[dep.ID] = true
				a.printRepairSuggestions(p, byDependency[dep.ID])
			} else {
				fmt.Fprintf(a.out, "  Path: %s\n", path)
			}
			if dep.MinVersion != "" {
				fmt.Fprintf(a.out, "  Required version: %s\n", dep.MinVersion)
			}
			if len(dep.Features) != 0 {
				fmt.Fprintf(a.out, "  Required features: %s\n", strings.Join(dep.Features, ", "))
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
			fmt.Fprintf(a.out, "    %s: %s\n", item.Manager, item.Package)
		}
	}
	if !shown {
		fmt.Fprintln(a.out, "  Install the dependency with your terminal or system package manager.")
	}
}
