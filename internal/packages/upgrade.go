package packages

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"

	"bmcshell/internal/librarydb"
)

var setName = regexp.MustCompile(`^set-[0-9]+$`)

type activeSet struct {
	Active         string  `json:"active"`
	Previous       *string `json:"previous,omitempty"`
	Group          string  `json:"group,omitempty"`
	Digest         string  `json:"digest,omitempty"`
	PreviousDigest string  `json:"previousDigest,omitempty"`
	Checkpoint     string  `json:"checkpoint,omitempty"`
}

type Upgrade struct {
	storeDir     string
	stage        string
	before       []Package
	damaged      []PackageDamage
	repaired     map[string]bool
	after        []Package
	changed      map[string]bool
	skipped      map[string]error
	beforeDigest string
	afterDigest  string
	general      bool
}

func (u *Upgrade) Packages() []Package         { return slices.Clone(u.after) }
func (u *Upgrade) OriginalPackages() []Package { return slices.Clone(u.before) }

func (u *Upgrade) SkippedLibraryUpdates() []error {
	var skipped []error
	for _, p := range u.before {
		if err := u.skipped[p.ID]; err != nil {
			skipped = append(skipped, err)
		}
	}
	return skipped
}

func (u *Upgrade) ChangedPackages() []Package {
	var changed []Package
	for _, p := range u.after {
		if u.changed[p.ID] {
			changed = append(changed, p)
		}
	}
	return changed
}

// SetSources binds changed packages to their verified catalog sources.
func (u *Upgrade) SetSources(sources map[string]string) error {
	if u.stage == "" {
		return errors.New("没有已准备的包集合")
	}
	for id, source := range sources {
		if u.skipped[id] != nil {
			continue
		}
		if !u.changed[id] || !originName.MatchString(source) {
			return fmt.Errorf("包 %s 的软件源绑定无效", id)
		}
	}
	for i, p := range u.after {
		if source := sources[p.ID]; source != "" && u.changed[p.ID] {

			u.after[i].Source = source
		}
	}
	var err error
	u.afterDigest, err = packageSetDigest(u.after)
	return err
}

// Close removes a prepared set if it has not been committed.
func (u *Upgrade) Close() error {
	if u.stage == "" {
		return nil
	}
	if err := os.RemoveAll(u.stage); err != nil {
		return err
	}
	u.stage = ""
	return nil
}

func (s *Store) setPath(name string) string {
	if name == "" {
		return s.dir
	}
	return filepath.Join(s.dir, ".sets", name)
}

func (s *Store) checkSetDirectory(name string) error {
	if !setName.MatchString(name) {
		return fmt.Errorf("包集合名称 %q 无效", name)
	}
	for _, dir := range []string{filepath.Join(s.dir, ".sets"), s.setPath(name)} {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("包集合目录 %q 无效", dir)
		}
	}
	return nil
}

// PrepareUpgrade validates a coordinated change to already installed packages.
func (s *Store) PrepareUpgrade(sources []string) (*Upgrade, error) {
	return s.prepareChanges(sources, nil, false)
}

// PrepareChanges stages the changed payloads and validates the resulting selection.
func (s *Store) PrepareChanges(sources []string, expected []Package) (*Upgrade, error) {
	return s.prepareChanges(sources, expected, true)
}

func (s *Store) prepareChanges(sources []string, expected []Package, general bool) (*Upgrade, error) {
	if len(sources) == 0 {
		return nil, errors.New("包变更需要至少一个包文件")
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	before, damaged, err := s.Inventory()
	if err != nil {
		return nil, err
	}
	if err := publishableDamages(damaged); err != nil {
		return nil, err
	}
	if !general && len(damaged) != 0 {
		return nil, damaged[0]
	}
	available := before
	if len(damaged) != 0 {
		available = availablePackages(before)
	}
	if general && !reflect.DeepEqual(available, expected) {
		return nil, errors.New("已安装的包发生变更，请重新生成软件源操作方案")
	}
	digest, err := packageSetDigest(before)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(s.dir), ".plugin-stage-")
	if err != nil {
		return nil, err
	}
	u := &Upgrade{
		storeDir: s.dir, stage: stage, before: before, beforeDigest: digest, damaged: damaged,
		changed: make(map[string]bool), repaired: make(map[string]bool), general: general,
	}
	if err := s.populateUpgrade(u, sources); err != nil {
		return nil, errors.Join(err, u.Close())
	}
	return u, nil
}

func (s *Store) populateUpgrade(u *Upgrade, sources []string) error {
	installed := make(map[string]Package, len(u.before))
	for _, p := range u.before {
		installed[p.ID] = p
	}
	for _, source := range sources {
		root, err := os.MkdirTemp(u.stage, "incoming-")
		if err != nil {
			return err
		}
		p, err := s.stagePackage(source, root)
		if err != nil {
			return fmt.Errorf("无法准备包 %q 的变更：%w", source, err)
		}
		old, exists := installed[p.ID]
		repair, err := repairSelectedRelease(p, u.damaged)
		if err != nil {
			return err
		}
		if repair {
			u.repaired[p.ID] = true
			for _, issue := range u.damaged {
				if issue.Package.ID == p.ID && originName.MatchString(issue.Package.Source) {
					p.Source = issue.Package.Source
					break
				}
			}
		}
		if !exists && !u.general {
			return fmt.Errorf("包 %s 尚未安装", p.ID)
		}
		if u.general && exists && !u.changed[p.ID] && p.Type == "library" && old.Type == "library" && p.Version == old.Version {
			old.Source = ""
			previous, err := packageSetDigest([]Package{old})
			if err != nil {
				return err
			}
			incoming, err := packageSetDigest([]Package{p})
			if err != nil {
				return err
			}
			if previous != incoming {
				return fmt.Errorf("业务库 %s %s 的内容有变更，请发布新版本", p.Name, p.Version)
			}
			continue
		}
		if u.changed[p.ID] || u.skipped[p.ID] != nil || exists && (CanonicalType(p.Type) != CanonicalType(old.Type) || p.Version == old.Version) {
			return fmt.Errorf("变更包 %s 须仅出现一次，保持包类型并更改已安装版本", p.ID)
		}
		if u.general && exists && p.Type == "library" {
			if err := checkLibraryUpdate(old, p); err != nil {
				var mismatch *libraryUpdateConflict
				if len(sources) == 1 || !errors.As(err, &mismatch) {
					return err
				}
				if u.skipped == nil {
					u.skipped = make(map[string]error)
				}
				u.skipped[p.ID] = err
				continue
			}
		}
		if exists {
			p.Source = old.Source
		}
		installed[p.ID] = p
		u.changed[p.ID] = true
	}
	for _, p := range installed {
		u.after = append(u.after, p)
	}
	sort.Slice(u.after, func(i, j int) bool { return u.after[i].ID < u.after[j].ID })
	if err := s.checkSet(u.after); err != nil {
		return errors.Join(append(u.SkippedLibraryUpdates(), err)...)
	}
	var err error
	u.afterDigest, err = packageSetDigest(u.after)
	return err
}

type libraryUpdateConflict struct{ name string }

func (e *libraryUpdateConflict) Error() string {
	return fmt.Sprintf("跳过 Library %s：新版包与现有库的结构不兼容，请备份工作库并检查发布包的更新规则", e.name)
}

func checkLibraryUpdate(old, next Package) error {
	mismatch := &libraryUpdateConflict{name: old.Name}
	if old.Library == nil {
		// The CLI validates and stages the legacy JSON-to-SQLite transition.
		return nil
	}
	if next.Library == nil {
		return mismatch
	}
	inspect := func(p Package) (librarydb.Snapshot, error) {
		path, err := p.File(p.Data)
		if err != nil {
			return librarydb.Snapshot{}, err
		}
		return librarydb.Validate(path, p.ID, *p.Library)
	}
	previous, err := inspect(old)
	if err != nil {
		return err
	}
	incoming, err := inspect(next)
	if err != nil {
		return err
	}
	if old.Library.StructurallyEqual(*next.Library) && len(librarydb.StructureDifferences(previous, incoming)) == 0 {
		return nil
	}
	if incoming.SchemaVersion > previous.SchemaVersion && next.LibraryMigrations[strconv.Itoa(previous.SchemaVersion)] != "" {
		return nil
	}
	return mismatch
}

func (s *Store) checkSet(list []Package) error {
	for i, p := range list {
		others := append(slices.Clone(list[:i]), list[i+1:]...)
		if err := checkInstall(p, others); err != nil {
			return err
		}
	}
	_, err := s.Environment(list)
	return err
}

// CommitUpgrade retains the previous selection and its checkpoint for rollback.
func (s *Store) CommitUpgrade(u *Upgrade, groupID, checkpoint string, beforeCommit func() error) error {
	if u == nil || u.general || !u.changed[groupID] {
		return errors.New("升级方案须属于当前包存储目录并包含选中的业务库")
	}
	if len(checkpoint) > 256 {
		return errors.New("业务库回滚检查点超过 256 字节")
	}
	if p, err := findPackage(u.after, groupID); err != nil || p.Library == nil {
		return errors.New("升级方案须包含 SQLite 业务库包")
	}
	return s.commitPrepared(u, groupID, checkpoint, nil, beforeCommit)
}

func (s *Store) CommitChanges(u *Upgrade, beforeCommit func() error) error {
	return s.CommitChangesWithLibraries(u, nil, beforeCommit)
}

// CommitChangesWithLibraries activates all package versions and prepared Library
// generations in one metadata transaction.
func (s *Store) CommitChangesWithLibraries(u *Upgrade, activations []LibraryActivation, beforeCommit func() error) error {
	if u == nil || !u.general {
		return errors.New("需要已准备的软件源包变更方案")
	}
	return s.commitPrepared(u, "", "", activations, beforeCommit)
}

func (s *Store) commitPrepared(u *Upgrade, groupID, checkpoint string, activations []LibraryActivation, beforeCommit func() error) error {
	if u.storeDir != s.dir || u.stage == "" {
		return errors.New("已准备的包集合不属于当前包存储目录")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	current, damaged, err := s.Inventory()
	if err != nil {
		return err
	}
	if !sameDamagedSelections(damaged, u.damaged) {
		return errors.New("选中的损坏包已变更，请重新准备包变更")
	}
	if err := verifyPackageDigest(current, u.beforeDigest); err != nil {
		return err
	}
	if err := s.verifyPackages(u.after, u.afterDigest); err != nil {
		return err
	}
	if err := validateLibrarySelections(u.after, activations); err != nil {
		return err
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return err
		}
	}
	var rollback *registryRollback
	if groupID != "" {
		previous, err := s.publishPackages(current)
		if err != nil {
			return err
		}
		rollback = &registryRollback{
			Group: groupID, Checkpoint: checkpoint, CurrentDigest: u.afterDigest,
			PreviousDigest: u.beforeDigest, Previous: previous,
		}
	}
	entries, err := s.publishSelections(u.after, u.damaged, u.repaired)
	if err != nil {
		return err
	}
	if err := s.writeRegistry(entries, activations, rollback, false); err != nil {
		return err
	}
	// The database now owns the selection. A failed cleanup cannot undo it.
	_ = u.Close()
	u.stage = ""
	return nil
}

// RollbackUpgrade reselects the most recent intact predecessor for the given Library.
func (s *Store) RollbackUpgrade(groupID string, beforeCommit func(previous Package, checkpoint string) error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	_, registered, err := s.registeredPackages()
	if err != nil {
		return err
	}
	if !registered {
		return s.rollbackLegacy(groupID, beforeCommit)
	}
	rollback, err := s.readRegistryRollback()
	if err != nil {
		return err
	}
	if rollback == nil || rollback.Group != groupID {
		return fmt.Errorf("当前没有业务库 %s 对应的包回滚记录", groupID)
	}
	current, err := s.List()
	if err != nil {
		return err
	}
	if err := s.verifyPackages(current, rollback.CurrentDigest); err != nil {
		return err
	}
	previous, err := s.loadRegisteredPackages(rollback.Previous)
	if err != nil {
		return err
	}
	if err := s.verifyPackages(previous, rollback.PreviousDigest); err != nil {
		return err
	}
	return s.commitRollback(previous, groupID, rollback.Checkpoint, beforeCommit)
}

func (s *Store) rollbackLegacy(groupID string, beforeCommit func(previous Package, checkpoint string) error) error {
	set, err := s.readActiveSet()
	if err != nil {
		return err
	}
	if set.Previous == nil || set.Group != groupID {
		return fmt.Errorf("当前没有业务库 %s 对应的包回滚记录", groupID)
	}
	if *set.Previous != "" {
		if err := s.checkSetDirectory(*set.Previous); err != nil {
			return err
		}
	}
	current, err := listSet(s.setPath(set.Active))
	if err != nil {
		return err
	}
	if err := s.verifyPackages(current, set.Digest); err != nil {
		return err
	}
	previous, err := listSet(s.setPath(*set.Previous))
	if err != nil {
		return err
	}
	if err := s.verifyPackages(previous, set.PreviousDigest); err != nil {
		return err
	}
	return s.commitRollback(previous, groupID, set.Checkpoint, beforeCommit)
}

func (s *Store) commitRollback(previous []Package, groupID, checkpoint string, beforeCommit func(Package, string) error) error {
	if beforeCommit != nil {
		p, err := findPackage(previous, groupID)
		if err != nil || p.ID != groupID {
			return fmt.Errorf("上一个包集合中没有业务库 %s", groupID)
		}
		if err := beforeCommit(p, checkpoint); err != nil {
			return err
		}
	}
	entries, err := s.publishPackages(previous)
	if err != nil {
		return err
	}
	return s.writeRegistry(entries, nil, nil, false)
}

func (s *Store) verifyPackages(list []Package, expected string) error {
	if err := verifyPackageDigest(list, expected); err != nil {
		return err
	}
	return s.checkSet(list)
}

func verifyPackageDigest(list []Package, expected string) error {
	digest, err := packageSetDigest(list)
	if err != nil {
		return err
	}
	if digest != expected {
		return errors.New("包集合的内容发生变更，请重新准备升级")
	}
	return nil
}

func packageSetDigest(list []Package) (string, error) {
	hash := sha256.New()
	for _, p := range list {
		fmt.Fprintln(hash, p.ID)
		if p.Source != "" {
			fmt.Fprintln(hash, originFile, p.Source)
		}
		var bytes int64
		var entries int
		err := filepath.WalkDir(p.Root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || path == p.Root {
				return err
			}
			if path == filepath.Join(p.Root, originFile) {
				return nil
			}
			entries++
			if entries > maxPackageFiles {
				return fmt.Errorf("包 %s 的条目数超过 %d", p.ID, maxPackageFiles)
			}
			rel, err := filepath.Rel(p.Root, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%q %d %d\n", filepath.ToSlash(rel), info.Mode(), info.Size())
			if entry.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() || info.Size() > maxPackageBytes-bytes {
				return fmt.Errorf("无法为包文件 %q 创建快照", path)
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			size, err := io.Copy(hash, io.LimitReader(f, info.Size()+1))
			if size != info.Size() && err == nil {
				return fmt.Errorf("包文件 %q 在读取期间发生变更", path)
			}
			bytes += size
			return err
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
