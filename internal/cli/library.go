package cli

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
	"bmcshell/internal/settings"
)

const maxLibrarySnapshot = 128 << 20

type libraryFiles struct {
	root, active, work, baseline string
	fingerprint                  [sha256.Size]byte
}

func (a *app) libraryFiles(p packages.Package) (libraryFiles, error) {
	state := libraryFiles{root: filepath.Join(a.home, "libraries", p.ID, p.Version)}
	active, err := readLibraryGeneration(state.root)
	if err != nil {
		return state, err
	}
	state.active = active
	if active == "" {
		state.work, err = p.File(p.Data)
		state.baseline = state.work
	} else {
		state.work = filepath.Join(state.root, active, "work.library.db")
		state.baseline = filepath.Join(state.root, active, "baseline.library.db")
	}
	return state, err
}

func readLibraryGeneration(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "active"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	active := strings.TrimSpace(string(data))
	if !strings.HasPrefix(active, "gen-") || filepath.Base(active) != active ||
		strings.ContainsAny(active, "/\\:") {
		return "", errors.New("invalid active Library generation")
	}
	return active, nil
}

func (a *app) libraryQuery(p packages.Package, keys []string) error {
	state, err := a.libraryFiles(p)
	if err != nil {
		return err
	}
	value, err := librarydb.Query(state.work, p.ID, *p.Library, keys)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, value)
	return nil
}

func validLibrarySource(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func (a *app) librarySource(args []string) error {
	if len(args) != 2 {
		return errors.New("用法 bmc library source <ID-or-name> <URL|--clear>")
	}
	p, err := a.findType(args[0], "library")
	if err != nil {
		return err
	}
	if p.Library == nil {
		return errors.New("请先安装 SQLite 格式的 Library 包")
	}
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	if args[1] == "--clear" {
		delete(cfg.LibrarySources, p.ID)
	} else {
		if !validLibrarySource(args[1]) {
			return errors.New("共享源需要可通过 GET 读取的 HTTP 或 HTTPS URL")
		}
		if cfg.LibrarySources == nil {
			cfg.LibrarySources = make(map[string]string)
		}
		cfg.LibrarySources[p.ID] = args[1]
	}
	return settings.Save(a.home, cfg)
}

func downloadLibrarySnapshot(source, path string) error {
	client := &http.Client{
		Timeout: 45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || !validLibrarySource(req.URL.String()) {
				return errors.New("invalid Library source redirect")
			}
			req.Header.Del("Referer")
			return nil
		},
	}
	response, err := client.Get(source)
	if err != nil {
		return errors.New("无法下载 Library 共享快照")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("共享快照返回 HTTP %d", response.StatusCode)
	}
	return copyLibrarySnapshot(path, response.Body)
}

func copyLibrarySnapshot(path string, source io.Reader) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	size, err := io.Copy(f, io.LimitReader(source, maxLibrarySnapshot+1))
	if err != nil {
		return err
	}
	if size > maxLibrarySnapshot {
		return errors.New("Library 快照超过 128 MiB")
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func copyLibraryFile(from, to string) error {
	file, err := os.Open(from)
	if err != nil {
		return err
	}
	defer file.Close()
	return copyLibrarySnapshot(to, file)
}

func (a *app) libraryRefresh(args []string) error {
	if len(args) < 1 {
		return libraryRefreshUsage()
	}
	var dryRun, replace bool
	var upgrades []string
	var packageSource string
	for flags := args[1:]; len(flags) != 0; flags = flags[1:] {
		switch flags[0] {
		case "--dry-run":
			dryRun = true
		case "--replace-all":
			replace = true
		case "--upgrade-with":
			if len(flags) < 2 || strings.HasPrefix(flags[1], "--") {
				return libraryRefreshUsage()
			}
			upgrades = append(upgrades, flags[1])
			flags = flags[1:]
		case "--source":
			if packageSource != "" || len(flags) < 2 || flags[1] == "" || strings.HasPrefix(flags[1], "--") {
				return libraryRefreshUsage()
			}
			packageSource = flags[1]
			flags = flags[1:]
		default:
			return libraryRefreshUsage()
		}
	}
	if replace && (dryRun || len(upgrades) != 0 || packageSource != "") ||
		len(upgrades) != 0 && packageSource != "" {
		return libraryRefreshUsage()
	}
	p, err := a.findType(args[0], "library")
	if err != nil {
		return err
	}
	if p.Library == nil {
		return errors.New("请先安装 SQLite 格式的 Library 包")
	}
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	source := cfg.LibrarySources[p.ID]
	if !validLibrarySource(source) {
		return errors.New("请先用 bmc library source 配置此库的共享 URL")
	}
	state, err := a.libraryFiles(p)
	if err != nil {
		return err
	}
	local, err := librarydb.Validate(state.work, p.ID, *p.Library)
	if err != nil {
		return fmt.Errorf("本地 Library: %w", err)
	}
	state.fingerprint, err = libraryFingerprint(state)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp("", "bmcshell-library-download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	remotePath := filepath.Join(stage, "remote.library.db")
	if err := downloadLibrarySnapshot(source, remotePath); err != nil {
		return err
	}
	remote, err := librarydb.Inspect(remotePath, p.ID)
	if err != nil {
		return fmt.Errorf("远端 Library: %w", err)
	}
	differences := librarydb.StructureDifferences(local, remote)
	if len(differences) > 0 {
		if err := a.reportLibraryMismatch(p, differences); err != nil {
			return err
		}
		if replace {
			return errors.New("全量覆盖要求表结构一致，请先完成 Library 联合升级")
		}
		return a.upgradeLibrary(p, state, local, remote, remotePath, stage, upgrades, packageSource, dryRun)
	}
	if len(upgrades) != 0 {
		return errors.New("远端库结构兼容，按普通同步处理即可")
	}
	if err := librarydb.ValidateSnapshot(remote, *p.Library); err != nil {
		return err
	}
	baseline, err := librarydb.Validate(state.baseline, p.ID, *p.Library)
	if err != nil {
		return fmt.Errorf("Library 比对基线: %w", err)
	}
	if differences := librarydb.StructureDifferences(local, baseline); len(differences) > 0 {
		return errors.New("本地 Library 与比对基线结构不一致")
	}
	plan, err := librarydb.Compare(state.work, state.baseline, remotePath, local, *p.Library, replace)
	if err != nil {
		return err
	}
	a.previewLibrary(plan, false)
	if dryRun {
		return nil
	}
	if len(plan.Changes) == 0 && !plan.BaselineChanged && remote.DataVersion == baseline.DataVersion {
		return nil
	}
	if !a.confirmLibrary(&plan, replace, "同步") {
		fmt.Fprintln(a.out, "已取消 Library 同步。")
		return nil
	}
	return a.publishLibrary(p, state, plan, remotePath, remote.DataVersion)
}

func libraryRefreshUsage() error {
	return errors.New("用法 bmc library refresh <ID-or-name> [--dry-run|--replace-all] [--source <名称> | --upgrade-with <文件> ...]")
}

func (a *app) reportLibraryMismatch(p packages.Package, differences []string) error {
	fmt.Fprintln(a.out, "检测到 Library 结构变更，已暂停同步。")
	for _, diff := range differences {
		fmt.Fprintln(a.out, "- "+diff)
	}
	list, err := a.store.List()
	if err != nil {
		return err
	}
	affected := libraryAffected(list, p.ID)
	for _, dep := range list {
		if dep.ID != p.ID && affected[dep.ID] {
			fmt.Fprintf(a.out, "受影响扩展 %s %s (%s)\n", dep.Name, dep.Version, dep.ID)
		}
	}
	return nil
}

func (a *app) previewLibrary(plan librarydb.Plan, details bool) {
	var added, updated, removed, conflicts int
	for _, change := range plan.Changes {
		switch {
		case change.Conflict:
			conflicts++
		case change.Local == nil:
			added++
		case change.Remote == nil:
			removed++
		default:
			updated++
		}
		if details {
			fmt.Fprintf(a.out, "%q %q 本地 %#v 远端 %#v\n", change.Table, fmt.Sprint(change.Key), change.Local, change.Remote)
		}
	}
	fmt.Fprintf(a.out, "新增 %d，修改 %d，建议删除 %d，冲突 %d。\n", added, updated, removed, conflicts)
}

func (a *app) confirmLibrary(plan *librarydb.Plan, replace bool, action string) bool {
	in := bufio.NewReader(a.in)
	for {
		fmt.Fprintf(a.out, "应用%s、查看差异或取消 [y/d/N] ", action)
		switch readChoice(in) {
		case "d":
			a.previewLibrary(*plan, true)
			continue
		case "y", "yes":
		default:
			return false
		}
		break
	}
	if replace {
		fmt.Fprint(a.out, "此操作会覆盖本地记录。请输入 REPLACE 再次确认 ")
		return readChoice(in) == "replace"
	}
	for i := range plan.Changes {
		change := &plan.Changes[i]
		if !change.Conflict {
			continue
		}
		fmt.Fprintf(a.out, "冲突 %s %q，保留本地、采用远端或取消 [l/r/N] ", change.Table, fmt.Sprint(change.Key))
		switch readChoice(in) {
		case "l":
		case "r":
			change.UseRemote = true
		default:
			return false
		}
	}
	var deletions int
	for _, change := range plan.Changes {
		if change.UseRemote && change.Remote == nil {
			fmt.Fprintf(a.out, "建议删除 %q 的键 %q\n", change.Table, fmt.Sprint(change.Key))
			deletions++
		}
	}
	if deletions > 0 {
		fmt.Fprintf(a.out, "确认删除上述 %d 条记录 [y/N] ", deletions)
		choice := readChoice(in)
		return choice == "y" || choice == "yes"
	}
	return true
}

func readChoice(in *bufio.Reader) string {
	line, _ := in.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(line))
}

func (a *app) publishLibrary(p packages.Package, state libraryFiles, plan librarydb.Plan, remote, version string) error {
	unlock, err := a.lockLibrary(p.ID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := checkLibraryGeneration(state); err != nil {
		return err
	}
	installed, err := a.store.Find(p.ID)
	if err != nil {
		return err
	}
	if installed.Version != p.Version {
		return errors.New("确认期间 Library 包版本已变更，请重新预览")
	}
	generation, err := stageLibraryGeneration(p, state.root, state.work, plan, remote, version)
	if err != nil {
		return err
	}
	if err := checkLibraryGeneration(state); err != nil {
		_ = os.RemoveAll(generation)
		return err
	}
	if err := activateLibraryGeneration(state.root, generation); err != nil {
		_ = os.RemoveAll(generation)
		return err
	}
	fmt.Fprintln(a.out, "Library 数据和比较基线已一起更新。")
	return nil
}

func (a *app) lockLibrary(id string) (func(), error) {
	root := filepath.Join(a.home, "libraries", id)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(root, ".sync-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return nil, fmt.Errorf("无法锁定 Library 同步: %w", err)
	}
	return func() { _ = os.Remove(lock) }, nil
}

func checkLibraryGeneration(state libraryFiles) error {
	active, err := readLibraryGeneration(state.root)
	if err != nil {
		return err
	}
	if active != state.active {
		return errors.New("确认期间 Library 已更新，请重新预览")
	}
	fingerprint, err := libraryFingerprint(state)
	if err != nil {
		return err
	}
	if fingerprint != state.fingerprint {
		return errors.New("确认期间 Library 文件发生修改，请重新预览")
	}
	return nil
}

func libraryFingerprint(state libraryFiles) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	hash := sha256.New()
	for i, path := range []string{state.work, state.baseline} {
		if i != 0 && path == state.work {
			continue
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				return result, errors.New("Library 有 SQLite 附属文件，请先结束本地编辑并保存独立快照")
			}
		}
		file, err := os.Open(path)
		if err != nil {
			return result, err
		}
		n, err := io.Copy(hash, io.LimitReader(file, maxLibrarySnapshot+1))
		closeErr := file.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return result, err
		}
		if n > maxLibrarySnapshot {
			return result, errors.New("Library 快照超过 128 MiB")
		}
	}
	hash.Sum(result[:0])
	return result, nil
}

func stageLibraryGeneration(p packages.Package, root, seed string, plan librarydb.Plan, remote, version string) (string, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	generation, err := os.MkdirTemp(root, "gen-")
	if err != nil {
		return "", err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(generation)
		}
	}()
	work := filepath.Join(generation, "work.library.db")
	if err := copyLibraryFile(seed, work); err != nil {
		return "", err
	}
	if err := plan.Apply(work, version); err != nil {
		return "", err
	}
	if _, err := librarydb.Validate(work, p.ID, *p.Library); err != nil {
		return "", err
	}
	if err := copyLibraryFile(remote, filepath.Join(generation, "baseline.library.db")); err != nil {
		return "", err
	}
	published = true
	return generation, nil
}

func activateLibraryGeneration(root, generation string) error {
	marker, err := os.CreateTemp(root, ".active-")
	if err != nil {
		return err
	}
	defer os.Remove(marker.Name())
	_, writeErr := fmt.Fprintln(marker, filepath.Base(generation))
	if writeErr == nil {
		writeErr = marker.Sync()
	}
	closeErr := marker.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(marker.Name(), filepath.Join(root, "active"))
}
