package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"bmcshell/internal/catalog"
	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func libraryDependent(p packages.Package, id string) bool {
	_, depends := p.Dependencies[id]
	_, declares := p.LibrarySchemas[id]
	return depends || declares
}

func libraryAffected(list []packages.Package, id string) map[string]bool {
	affected := map[string]bool{id: true}
	for {
		before := len(affected)
		for _, p := range list {
			if affected[p.ID] {
				continue
			}
			for dep := range affected {
				if libraryDependent(p, dep) {
					affected[p.ID] = true
					break
				}
			}
		}
		if before == len(affected) {
			return affected
		}
	}
}

type libraryCatalogPackage struct {
	fromVersion, fromSource, source string
}

func (a *app) upgradeLibrary(current packages.Package, state libraryFiles, local, remote librarydb.Snapshot, remotePath, stage string, files []string, source string, dryRun bool) error {
	if remote.SchemaVersion <= local.SchemaVersion {
		return errors.New("远端结构版本未提高，请让发布者修正快照的结构和版本声明")
	}
	var indexed map[string]libraryCatalogPackage
	if len(files) == 0 {
		var err error
		files, indexed, err = a.catalogLibraryPackages(current.ID, remote.SchemaVersion, source, stage)
		if err != nil {
			return err
		}
	}
	upgrade, err := a.store.PrepareUpgrade(files)
	if err != nil {
		return err
	}
	defer upgrade.Close()
	if len(indexed) != 0 {
		origins := make(map[string]string, len(indexed))
		for id, planned := range indexed {
			origins[id] = planned.source
		}
		if err := upgrade.SetSources(origins); err != nil {
			return err
		}
	}
	candidate, err := a.checkLibraryUpgrade(current, remote.SchemaVersion, upgrade, indexed)
	if err != nil {
		return err
	}
	template, err := candidate.File(candidate.Data)
	if err != nil {
		return err
	}
	target, err := librarydb.Validate(template, candidate.ID, *candidate.Library)
	if err != nil {
		return err
	}
	if diff := librarydb.StructureDifferences(target, remote); len(diff) > 0 {
		return fmt.Errorf("新版 Library 包与共享快照结构不匹配（%s）", strings.Join(diff, "；"))
	}
	if err := librarydb.ValidateSnapshot(remote, *candidate.Library); err != nil {
		return err
	}
	migration, err := candidate.File(candidate.LibraryMigrations[strconv.Itoa(local.SchemaVersion)])
	if err != nil {
		return fmt.Errorf("新版 Library 包需要从结构版本 %d 迁移的 SQL 文件: %w", local.SchemaVersion, err)
	}
	baseline, err := librarydb.Validate(state.baseline, current.ID, *current.Library)
	if err != nil {
		return err
	}
	if len(librarydb.StructureDifferences(local, baseline)) != 0 {
		return errors.New("本地 Library 与比对基线结构不一致")
	}
	migratedWork := filepath.Join(stage, "migrated-work.library.db")
	migratedBaseline := filepath.Join(stage, "migrated-baseline.library.db")
	for _, file := range []struct{ old, staged string }{
		{state.work, migratedWork},
		{state.baseline, migratedBaseline},
	} {
		if err := copyLibraryFile(file.old, file.staged); err != nil {
			return err
		}
		if err := librarydb.Migrate(file.staged, migration, local.SchemaVersion, remote.SchemaVersion); err != nil {
			return err
		}
		result, err := librarydb.Validate(file.staged, candidate.ID, *candidate.Library)
		if err != nil {
			return err
		}
		if diff := librarydb.StructureDifferences(target, result); len(diff) > 0 {
			return fmt.Errorf("迁移后的本地库与新版包结构不匹配（%s）", strings.Join(diff, "；"))
		}
	}
	plan, err := librarydb.Compare(migratedWork, migratedBaseline, remotePath, target, *candidate.Library, false)
	if err != nil {
		return err
	}
	a.previewLibrary(plan, false)
	if dryRun {
		return nil
	}
	if !a.confirmLibrary(&plan, false, "联合升级") {
		fmt.Fprintln(a.out, "已取消 Library 联合升级。")
		return nil
	}
	return a.publishLibraryUpgrade(current, candidate, state, upgrade, migratedWork, plan, remotePath, remote.DataVersion)
}

func (a *app) catalogLibraryPackages(id string, schemaVersion int, source, stage string) ([]string, map[string]libraryCatalogPackage, error) {
	entries, err := catalog.Open(a.home).Entries()
	if err != nil {
		return nil, nil, fmt.Errorf("读取软件源索引: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil, errors.New("没有可用的软件源索引。请先运行 bmc plugin update，或用 --upgrade-with 提供本地包")
	}
	installed, err := a.store.List()
	if err != nil {
		return nil, nil, err
	}
	selected, err := catalog.ResolveLibrary(entries, installed, id, schemaVersion, source)
	if err != nil {
		return nil, nil, fmt.Errorf("软件源无法解析 Library 联合升级（可用 --upgrade-with 提供本地包）: %w", err)
	}
	current := make(map[string]packages.Package, len(installed))
	for _, p := range installed {
		current[p.ID] = p
	}
	files := make([]string, 0, len(selected))
	indexed := make(map[string]libraryCatalogPackage, len(selected))
	for _, entry := range selected {
		path, err := catalog.Download(context.Background(), entry, stage)
		if err != nil {
			return nil, nil, fmt.Errorf("下载 %s %s（源 %s）: %w", entry.Name, entry.Version, entry.Source, err)
		}
		files = append(files, path)
		old := current[entry.ID]
		indexed[entry.ID] = libraryCatalogPackage{fromVersion: old.Version, fromSource: old.Source, source: entry.Source}
	}
	return files, indexed, nil
}

func (a *app) checkLibraryUpgrade(current packages.Package, version int, upgrade *packages.Upgrade, indexed map[string]libraryCatalogPackage) (packages.Package, error) {
	original := make(map[string]packages.Package)
	before, after := upgrade.OriginalPackages(), upgrade.Packages()
	for _, p := range before {
		original[p.ID] = p
	}
	if original[current.ID].Version != current.Version {
		return packages.Package{}, errors.New("确认期间 Library 包版本已变更，请重新预览")
	}
	affected := libraryAffected(slices.Concat(before, after), current.ID)
	var candidate packages.Package
	for _, p := range after {
		old := original[p.ID]
		planned, fromIndex := indexed[p.ID]
		if fromIndex && (old.Version != planned.fromVersion || old.Source != planned.fromSource) {
			return candidate, fmt.Errorf("规划期间扩展 %s 的版本或软件源已变化，请重新规划", p.Name)
		}
		if p.ID == current.ID {
			candidate = p
			continue
		}
		if p.Type == "library" && old.Version != p.Version {
			return candidate, fmt.Errorf("此计划只能迁移当前选中的 Library，%s 需要单独同步", p.Name)
		}
		if !affected[p.ID] {
			if old.Version != p.Version {
				return candidate, fmt.Errorf("扩展 %s 不依赖此 Library，不能纳入联合升级", p.Name)
			}
			continue
		}
		direct := libraryDependent(old, current.ID) || libraryDependent(p, current.ID)
		if direct && !p.LibrarySchemas[current.ID].Supports(version) {
			return candidate, fmt.Errorf("扩展 %s 需要支持 Library 结构 %d 的版本", p.Name, version)
		}
		if fromIndex {
			fmt.Fprintf(a.out, "工具 %s %s → %s（源 %s）\n", p.Name, old.Version, p.Version, planned.source)
		} else {
			fmt.Fprintf(a.out, "工具 %s %s → %s\n", p.Name, old.Version, p.Version)
		}
	}
	if candidate.Library == nil || candidate.Version == current.Version || candidate.Library.SchemaVersion != version {
		return candidate, fmt.Errorf("需要同一 UUID 的新版 Library 包，并声明结构 %d", version)
	}
	fmt.Fprintf(a.out, "Library %s %s → %s（结构 %d → %d）\n",
		candidate.Name, current.Version, candidate.Version, current.Library.SchemaVersion, version)
	if source := indexed[candidate.ID].source; source != "" {
		fmt.Fprintf(a.out, "Library 软件源 %s\n", source)
	}
	return candidate, nil
}

func (a *app) publishLibraryUpgrade(current, candidate packages.Package, state libraryFiles, upgrade *packages.Upgrade,
	seed string, plan librarydb.Plan, remote, version string) error {
	unlock, err := a.lockLibrary(current.ID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := checkLibraryGeneration(state); err != nil {
		return err
	}
	root := filepath.Join(a.home, "libraries", candidate.ID, candidate.Version)
	previous, err := readLibraryGeneration(root)
	if err != nil {
		return err
	}
	generation, err := stageLibraryGeneration(candidate, root, seed, plan, remote, version)
	if err != nil {
		return err
	}
	activated := false
	checkpoint := libraryCheckpoint(state)
	err = a.store.CommitUpgrade(upgrade, current.ID, checkpoint, func() error {
		if err := checkLibraryGeneration(state); err != nil {
			return err
		}
		if err := activateLibraryGeneration(root, generation); err != nil {
			return err
		}
		activated = true
		return nil
	})
	if err != nil {
		var restoreErr error
		if activated {
			if previous == "" {
				restoreErr = os.Remove(filepath.Join(root, "active"))
			} else {
				restoreErr = activateLibraryGeneration(root, previous)
			}
		}
		if restoreErr == nil {
			_ = os.RemoveAll(generation)
		}
		return errors.Join(err, restoreErr)
	}
	fmt.Fprintln(a.out, "已一起切换 Library、数据库、比较基线和兼容工具。")
	return nil
}

func (a *app) libraryRollback(args []string) error {
	if len(args) != 1 {
		return errors.New("用法 bmc library rollback <ID-or-name>")
	}
	p, err := a.findType(args[0], "library")
	if err != nil {
		return err
	}
	fmt.Fprint(a.out, "回退此 Library 最近一次联合升级及其工具 [y/N] ")
	switch readChoice(bufio.NewReader(a.in)) {
	case "y", "yes":
	default:
		return nil
	}
	unlock, err := a.lockLibrary(p.ID)
	if err != nil {
		return err
	}
	defer unlock()
	active, err := a.store.Find(p.ID)
	if err != nil {
		return err
	}
	if active.Version != p.Version {
		return errors.New("确认期间 Library 包版本已变更，请重新确认")
	}
	if err := a.store.RollbackUpgrade(p.ID, func(previous packages.Package, checkpoint string) error {
		state, err := a.libraryFiles(previous)
		if err != nil {
			return err
		}
		state.fingerprint, err = libraryFingerprint(state)
		if err != nil {
			return err
		}
		if libraryCheckpoint(state) != checkpoint {
			return errors.New("回退所需的旧版工作库或基线已变更")
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "已恢复升级前的 Library、数据库、比较基线和工具。")
	return nil
}

func libraryCheckpoint(state libraryFiles) string {
	return fmt.Sprintf("%s:%x", state.active, state.fingerprint)
}
