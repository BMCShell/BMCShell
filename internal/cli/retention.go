package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/release"
)

type historyPlan struct {
	Core      []string
	Packages  []string
	Libraries []string
}

// history serves the packaged offline maintenance script.
func (a *app) history(args []string) error {
	if len(args) != 1 || os.Getenv("BMCSHELL_BRIDGE_MAINTENANCE") != "1" {
		return errors.New("请通过离线维护脚本清理历史版本")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	root, portable := release.PortableRoot(executable)
	if !portable {
		return errors.New("历史版本维护需要便携版安装")
	}

	switch args[0] {
	case "verify":
		return a.verifyHistoryBackup()
	case "retire":
		var expected []string
		if err := json.NewDecoder(io.LimitReader(a.in, 4<<20)).Decode(&expected); err != nil {
			return fmt.Errorf("无法读取已验证的业务库清理计划：%w", err)
		}
		return a.store.RetireLibraryHistory(expected)
	case "preview", "plan":
		var plan historyPlan
		plan.Core, err = release.PlanRetention(root)
		if err != nil {
			return err
		}
		plan.Packages, err = a.store.PlanPayloadRetention()
		if err != nil {
			return err
		}
		if args[0] == "preview" {
			plan.Libraries, err = a.store.PreviewLibraryRetention()
		} else {
			plan.Libraries, err = a.store.PlanLibraryRetention()
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(a.out).Encode(plan)
	default:
		return errors.New("未知的离线维护操作")
	}
}

func (a *app) verifyHistoryBackup() error {
	installed, damaged, err := a.store.Inventory()
	if err != nil {
		return err
	}
	if len(damaged) != 0 {
		return fmt.Errorf("扩展包 %s 已损坏，无法验证备份", damaged[0].Package.ID)
	}
	for _, p := range installed {
		if p.Library == nil {
			continue
		}
		state, err := a.libraryFiles(p)
		if err != nil {
			return err
		}
		if _, err := librarydb.ValidateWorking(state.work, p.ID, *p.Library); err != nil {
			return fmt.Errorf("无法验证业务库 %s 的工作库：%w", p.Name, err)
		}
		if _, err := librarydb.Validate(state.baseline, p.ID, *p.Library); err != nil {
			return fmt.Errorf("无法验证业务库 %s 的基线：%w", p.Name, err)
		}
	}
	return nil
}
