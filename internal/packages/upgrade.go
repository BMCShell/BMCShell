package packages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"

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
	from         string
	before       []Package
	after        []Package
	changed      map[string]bool
	beforeDigest string
	afterDigest  string
	general      bool
}

func (u *Upgrade) Packages() []Package         { return slices.Clone(u.after) }
func (u *Upgrade) OriginalPackages() []Package { return slices.Clone(u.before) }

// SetSources binds changed packages to their verified catalog sources in the staged set.
func (u *Upgrade) SetSources(sources map[string]string) error {
	if u.stage == "" {
		return errors.New("no prepared package set")
	}
	for id, source := range sources {
		if !u.changed[id] || !originName.MatchString(source) {
			return fmt.Errorf("invalid source binding for package %s", id)
		}
	}
	for i, p := range u.after {
		if source := sources[p.ID]; source != "" {
			if err := writeSource(p.Root, source); err != nil {
				return err
			}
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

func (s *Store) readActiveSet() (activeSet, error) {
	path := filepath.Join(s.dir, ".active-set")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return activeSet{}, nil
	}
	if err != nil {
		return activeSet{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return activeSet{}, errors.New("invalid active package-set marker")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return activeSet{}, err
	}
	var set *activeSet
	if err := json.Unmarshal(data, &set); err != nil {
		return activeSet{}, fmt.Errorf("invalid active package-set marker: %w", err)
	}
	if set == nil {
		return activeSet{}, errors.New("invalid active package-set marker")
	}
	if set.Active != "" {
		if err := s.checkSetDirectory(set.Active); err != nil {
			return activeSet{}, err
		}
	}
	if set.Previous != nil {
		if *set.Previous == set.Active || !packageID.MatchString(set.Group) || set.Digest == "" || set.PreviousDigest == "" || len(set.Checkpoint) > 256 {
			return activeSet{}, errors.New("invalid package rollback marker")
		}
		if *set.Previous != "" && !setName.MatchString(*set.Previous) {
			return activeSet{}, errors.New("invalid previous package-set name")
		}
	}
	return *set, nil
}

func (s *Store) checkSetDirectory(name string) error {
	if !setName.MatchString(name) {
		return fmt.Errorf("invalid package-set name %q", name)
	}
	for _, dir := range []string{filepath.Join(s.dir, ".sets"), s.setPath(name)} {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("invalid package-set directory %q", dir)
		}
	}
	return nil
}

func (s *Store) writeActiveSet(set activeSet) error {
	f, err := os.CreateTemp(s.dir, ".active-set-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(set)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(s.dir, ".active-set"))
}

// PrepareUpgrade builds a complete candidate package set without activating it.
func (s *Store) PrepareUpgrade(sources []string) (*Upgrade, error) {
	return s.prepareChanges(sources, nil, false)
}

// PrepareChanges stages catalog installs and ordinary upgrades as one package set.
func (s *Store) PrepareChanges(sources []string, expected []Package) (*Upgrade, error) {
	return s.prepareChanges(sources, expected, true)
}

func (s *Store) prepareChanges(sources []string, expected []Package, general bool) (*Upgrade, error) {
	if len(sources) == 0 {
		return nil, errors.New("package change needs at least one package file")
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	set, err := s.readActiveSet()
	if err != nil {
		return nil, err
	}
	before, err := listSet(s.setPath(set.Active))
	if err != nil {
		return nil, err
	}
	if general && !reflect.DeepEqual(before, expected) {
		return nil, errors.New("installed packages changed; resolve the catalog plan again")
	}
	digest, err := packageSetDigest(before)
	if err != nil {
		return nil, err
	}
	sets := filepath.Join(s.dir, ".sets")
	if err := os.MkdirAll(sets, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(sets); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("invalid package-set directory %q", sets)
	}
	stage, err := os.MkdirTemp(sets, "set-")
	if err != nil {
		return nil, err
	}
	u := &Upgrade{
		storeDir: s.dir, stage: stage, from: set.Active,
		before: before, beforeDigest: digest, changed: make(map[string]bool), general: general,
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
		root, err := os.MkdirTemp(u.stage, ".incoming-")
		if err != nil {
			return err
		}
		p, err := s.stagePackage(source, root)
		if err != nil {
			return fmt.Errorf("upgrade package %q: %w", source, err)
		}
		old, exists := installed[p.ID]
		if !exists && !u.general {
			return fmt.Errorf("package %s is not installed", p.ID)
		}
		if u.changed[p.ID] || exists && (p.Type != old.Type || p.Version == old.Version) {
			return fmt.Errorf("changed package %s must appear once, preserve its type, and change its installed version", p.ID)
		}
		if u.general && exists && p.Type == "library" {
			if err := checkLibraryUpdate(old, p); err != nil {
				return err
			}
		}
		if old.Source != "" {
			if err := writeSource(root, old.Source); err != nil {
				return err
			}
		}
		if err := os.Rename(root, filepath.Join(u.stage, p.ID)); err != nil {
			return err
		}
		u.changed[p.ID] = true
	}
	for _, p := range u.before {
		if !u.changed[p.ID] {
			root := filepath.Join(u.stage, p.ID)
			if err := copyPackageDirectory(p.Root, root, true); err != nil {
				return err
			}
			if p.Source != "" {
				if err := writeSource(root, p.Source); err != nil {
					return err
				}
			}
		}
	}
	after, err := listSet(u.stage)
	if err != nil {
		return err
	}
	if err := s.checkSet(after); err != nil {
		return err
	}
	u.after = after
	u.afterDigest, err = packageSetDigest(after)
	return err
}

func checkLibraryUpdate(old, next Package) error {
	mismatch := fmt.Errorf("Library %s changes its schema; use bmc library refresh for a coordinated upgrade", old.Name)
	if !reflect.DeepEqual(old.Library, next.Library) {
		return mismatch
	}
	if old.Library == nil {
		return nil
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
	if len(librarydb.StructureDifferences(previous, incoming)) != 0 {
		return mismatch
	}
	return nil
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

// CommitUpgrade publishes the new set after revalidating the inputs and running the data commit.
func (s *Store) CommitUpgrade(u *Upgrade, groupID, checkpoint string, beforeCommit func() error) error {
	if u == nil || u.general || !u.changed[groupID] {
		return errors.New("upgrade must belong to this store and include the selected Library")
	}
	if len(checkpoint) > 256 {
		return errors.New("Library rollback checkpoint exceeds 256 bytes")
	}
	if p, err := findPackage(u.after, groupID); err != nil || p.Library == nil {
		return errors.New("upgrade group must be a SQLite Library package")
	}
	return s.commitPrepared(u, groupID, checkpoint, beforeCommit)
}

// CommitChanges activates a catalog package set after any compatible Library data
// has been prepared by the caller.
func (s *Store) CommitChanges(u *Upgrade, beforeCommit func() error) error {
	if u == nil || !u.general {
		return errors.New("expected a prepared catalog package change")
	}
	return s.commitPrepared(u, "", "", beforeCommit)
}

func (s *Store) commitPrepared(u *Upgrade, groupID, checkpoint string, beforeCommit func() error) error {
	if u.storeDir != s.dir || u.stage == "" {
		return errors.New("prepared package set does not belong to this store")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.readActiveSet()
	if err != nil {
		return err
	}
	if set.Active != u.from {
		return errors.New("active package set changed during upgrade")
	}
	if _, err := s.verifySet(s.setPath(set.Active), u.beforeDigest); err != nil {
		return err
	}
	if _, err := s.verifySet(u.stage, u.afterDigest); err != nil {
		return err
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return err
		}
	}
	next := activeSet{Active: filepath.Base(u.stage)}
	if groupID != "" {
		next.Previous, next.Group = &set.Active, groupID
		next.Digest, next.PreviousDigest, next.Checkpoint = u.afterDigest, u.beforeDigest, checkpoint
	}
	if err := s.writeActiveSet(next); err != nil {
		return err
	}
	u.stage = "" // Published sets remain available for rollback.
	return nil
}

// RollbackUpgrade reselects the most recent intact predecessor for the given Library.
func (s *Store) RollbackUpgrade(groupID string, beforeCommit func(previous Package, checkpoint string) error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.readActiveSet()
	if err != nil {
		return err
	}
	if set.Previous == nil || set.Group != groupID {
		return fmt.Errorf("no current package rollback belongs to Library %s", groupID)
	}
	if *set.Previous != "" {
		if err := s.checkSetDirectory(*set.Previous); err != nil {
			return err
		}
	}
	if _, err := s.verifySet(s.setPath(set.Active), set.Digest); err != nil {
		return err
	}
	previous, err := s.verifySet(s.setPath(*set.Previous), set.PreviousDigest)
	if err != nil {
		return err
	}
	if beforeCommit != nil {
		p, err := findPackage(previous, groupID)
		if err != nil || p.ID != groupID {
			return fmt.Errorf("previous package set does not contain Library %s", groupID)
		}
		if err := beforeCommit(p, set.Checkpoint); err != nil {
			return err
		}
	}
	return s.writeActiveSet(activeSet{Active: *set.Previous})
}

func (s *Store) verifySet(root, expected string) ([]Package, error) {
	list, err := listSet(root)
	if err != nil {
		return nil, err
	}
	digest, err := packageSetDigest(list)
	if err != nil {
		return nil, err
	}
	if digest != expected {
		return nil, errors.New("package-set contents changed; prepare the upgrade again")
	}
	return list, s.checkSet(list)
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
				return fmt.Errorf("package %s exceeds %d entries", p.ID, maxPackageFiles)
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
				return fmt.Errorf("cannot snapshot package file %q", path)
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			size, err := io.Copy(hash, io.LimitReader(f, info.Size()+1))
			if size != info.Size() && err == nil {
				return fmt.Errorf("package file %q changed while reading", path)
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
