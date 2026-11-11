package packages

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"bmcshell/internal/filelock"
	"bmcshell/internal/librarydb"
)

const (
	maxPackageBytes = 128 << 20
	maxPackageFiles = 4096
)

type Store struct {
	dir string
}

func Open(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("package store root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Store{dir: filepath.Join(root, "packages")}, nil
}

// PackageDamage identifies a selected package whose release files cannot be
// used. Its Package holds the identity from the registry, without capabilities.
type PackageDamage struct {
	Package Package
	Err     error
	legacy  bool
}

func (d PackageDamage) entry() registryEntry {
	p := d.Package
	return registryEntry{ID: p.ID, Kind: p.Type, Version: p.Version, Source: p.Source, Name: p.Name}
}

func (d PackageDamage) Error() string {
	p := d.Package
	if p.Name != "" {
		return fmt.Sprintf("%s@%s (%s) at %s: %v", p.Name, p.Version, p.ID, p.Root, d.Err)
	}
	return fmt.Sprintf("%s %s at %s: %v", p.ID, p.Version, p.Root, d.Err)
}

func (s *Store) List() ([]Package, error) {
	installed, damaged, err := s.Inventory()
	if err != nil || len(damaged) == 0 {
		return installed, err
	}
	return availablePackages(installed), nil
}

// Inventory returns validated payloads and individual damaged selections. An
// unreadable registry is a separate error because its selections are unknown.
func (s *Store) Inventory() ([]Package, []PackageDamage, error) {
	entries, registered, err := s.registeredPackages()
	if err != nil {
		return nil, nil, err
	}
	if registered {
		var installed []Package
		var damaged []PackageDamage
		for _, entry := range entries {
			p, err := s.loadRegisteredPackage(entry)
			if err != nil {
				damaged = append(damaged, PackageDamage{Package: p, Err: err})
				continue
			}
			installed = append(installed, p)
		}
		return installed, damaged, nil
	}
	set, err := s.readActiveSet()
	if err != nil {
		return nil, nil, err
	}
	return inventorySet(s.setPath(set.Active))
}

func listSet(root string) ([]Package, error) {
	installed, damaged, err := inventorySet(root)
	if err != nil {
		return nil, err
	}
	if len(damaged) != 0 {
		return nil, damaged[0]
	}
	return installed, nil
}

func inventorySet(root string) ([]Package, []PackageDamage, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var installed []Package
	var damaged []PackageDamage
	for _, entry := range entries {
		if !packageID.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		p, err := loadLegacyPackage(path, entry)
		if err != nil {
			damaged = append(damaged, PackageDamage{
				Package: Package{Manifest: Manifest{ID: entry.Name()}, Root: path}, Err: err, legacy: true,
			})
			continue
		}
		installed = append(installed, p)
	}
	return installed, damaged, nil
}

func loadLegacyPackage(path string, entry os.DirEntry) (Package, error) {
	if !entry.IsDir() {
		return Package{}, fmt.Errorf("invalid package directory %s", path)
	}
	p, err := loadPackage(path)
	if err != nil {
		return Package{}, err
	}
	if p.ID != entry.Name() {
		return Package{}, fmt.Errorf("installed package ID %s does not match directory %s", p.ID, entry.Name())
	}
	p.Source, err = readSource(path)
	return p, err
}

func publishableDamages(damaged []PackageDamage) error {
	for _, issue := range damaged {
		if issue.legacy {
			return fmt.Errorf("restore the old-layout package before changing registrations: %w", issue)
		}
	}
	return nil
}

// FindInstalled identifies a selection for package management, even if damaged.
func (s *Store) FindInstalled(selector string) (Package, error) {
	installed, damaged, err := s.Inventory()
	if err != nil {
		return Package{}, err
	}
	return findSelection(installed, damaged, selector)
}

func findSelection(installed []Package, damaged []PackageDamage, selector string) (Package, error) {
	for _, issue := range damaged {
		installed = append(installed, issue.Package)
	}
	return findPackage(installed, selector)
}

func (s *Store) Find(selector string) (Package, error) {
	installed, err := s.List()
	if err != nil {
		return Package{}, err
	}
	return findPackage(installed, selector)
}

// FindName selects a public package name, optionally within a package type.
func (s *Store) FindName(name, kind string) (Package, error) {
	installed, err := s.List()
	if err != nil {
		return Package{}, err
	}
	var shortNames, aliases, fullNames []Package
	for _, p := range installed {
		if kind != "" && CanonicalType(p.Type) != kind {
			continue
		}
		if strings.EqualFold(name, p.ID) {
			return p, nil
		}
		if name == p.Name {
			fullNames = append(fullNames, p)
		}
		if kind != "book" && kind != "library" {
			continue
		}
		if name == p.DataName() {
			shortNames = append(shortNames, p)
		}
		if kind == "book" && isBookAlias(p.ID, name) {
			aliases = append(aliases, p)
		}
	}
	for _, matches := range [][]Package{shortNames, fullNames, aliases} {
		switch len(matches) {
		case 0:
			continue
		case 1:
			return matches[0], nil
		default:
			return Package{}, &AmbiguousNameError{Name: name, Matches: matches}
		}
	}
	return Package{}, fmt.Errorf("package %q is not installed", name)
}

// The shipped Book IDs retain their v2 command spellings across package updates.
func isBookAlias(id, name string) bool {
	switch id {
	case "4f39cb16-7e66-4fe2-9afe-fdfa56e7f005":
		return name == "oid" || name == "snmp-oid"
	case "8822fff1-51d7-4e2f-a5e7-dec7b344f2cf":
		return name == "oem_ipmi" || name == "oem-ipmi"
	default:
		return false
	}
}

type AmbiguousNameError struct {
	Name    string
	Matches []Package
}

func (e *AmbiguousNameError) Error() string {
	candidates := make([]string, len(e.Matches))
	for i, p := range e.Matches {
		source := p.Source
		if source == "" {
			source = "local"
		}
		candidates[i] = fmt.Sprintf("%s %s %s (%s)", p.Type, source, p.Version, p.ID)
	}
	return fmt.Sprintf("ambiguous package name %q; use a UUID from: %s", e.Name, strings.Join(candidates, ", "))
}

func findPackage(installed []Package, selector string) (Package, error) {
	var matches []Package
	for _, p := range installed {
		if strings.EqualFold(selector, p.ID) {
			return p, nil
		}
		if selector == p.Name {
			matches = append(matches, p)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Package{}, &AmbiguousNameError{Name: selector, Matches: matches}
	}
	return Package{}, fmt.Errorf("package %q is not installed", selector)
}

func (s *Store) lock() (func(), error) {
	root := filepath.Dir(s.dir)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	unlock, err := filelock.Acquire(filepath.Join(root, ".package-store.lock"))
	if err != nil {
		return nil, fmt.Errorf("package store lock: %w", err)
	}
	return unlock, nil
}

// Install validates a local package before publishing its registered release.
func (s *Store) Install(source string) (Package, error) {
	unlock, err := s.lock()
	if err != nil {
		return Package{}, err
	}
	defer unlock()
	stage, err := os.MkdirTemp(filepath.Dir(s.dir), ".install-")
	if err != nil {
		return Package{}, err
	}
	defer os.RemoveAll(stage)
	p, err := s.stagePackage(source, stage)
	if err != nil {
		return Package{}, err
	}
	installed, damaged, err := s.Inventory()
	if err != nil {
		return Package{}, err
	}
	if err := publishableDamages(damaged); err != nil {
		return Package{}, err
	}
	repair, err := repairSelectedRelease(p, damaged)
	if err != nil {
		return Package{}, err
	}
	next := append(installed, p)
	if err := s.checkSet(next); err != nil {
		return Package{}, err
	}
	entries, err := s.publishSelections(next, damaged, map[string]bool{p.ID: repair})
	if err != nil {
		return Package{}, err
	}
	p.Root, err = s.payloadPath(entries[len(entries)-1], false)
	if err != nil {
		return Package{}, err
	}
	if err := s.writeRegistry(entries, nil, nil, true); err != nil {
		return Package{}, err
	}
	return p, nil
}

func (s *Store) stagePackage(source, stage string) (Package, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return Package{}, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Package{}, err
	}
	if info.IsDir() {
		resolvedSource, err := filepath.EvalSymlinks(source)
		if err != nil {
			return Package{}, err
		}
		resolvedStage, err := filepath.EvalSymlinks(stage)
		if err != nil {
			return Package{}, err
		}
		if containsDir(resolvedSource, resolvedStage) {
			return Package{}, fmt.Errorf("package source cannot contain the package staging directory")
		}
	}
	if info.IsDir() {
		err = copyDirectory(source, stage)
	} else if info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(source), ".zip") {
		err = extractZIP(source, stage)
	} else {
		err = fmt.Errorf("package source must be a directory or ZIP")
	}
	if err != nil {
		return Package{}, err
	}
	p, err := loadPackage(stage)
	if err != nil {
		return Package{}, err
	}
	if p.Type == "book" && !strings.HasSuffix(strings.ToLower(p.Data), ".book.db") {
		return Package{}, fmt.Errorf("Book data file must end in .book.db: %q", p.Data)
	}
	if err := p.validateDocumentation(); err != nil {
		return Package{}, err
	}
	if p.Library != nil {
		path, err := p.File(p.Data)
		if err != nil {
			return Package{}, err
		}
		if _, err := librarydb.Validate(path, p.ID, *p.Library); err != nil {
			return Package{}, fmt.Errorf("invalid Library data: %w", err)
		}
	}
	return p, nil
}

func containsDir(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func checkInstall(p Package, installed []Package) error {
	versions := make(map[string]string, len(installed))
	schemas := make(map[string]int, len(installed))
	for _, previous := range installed {
		if previous.ID == p.ID {
			return fmt.Errorf("package %s is already installed at %s", p.ID, previous.Version)
		}
		versions[previous.ID] = previous.Version
		if previous.Library != nil {
			schemas[previous.ID] = previous.Library.SchemaVersion
		}
		for name := range p.Provides.Tools {
			if _, exists := previous.Provides.Tools[name]; exists {
				return fmt.Errorf("tool %q is already provided by %s", name, previous.ID)
			}
		}
		for name := range p.Provides.Resolvers {
			if _, exists := previous.Provides.Resolvers[name]; exists {
				return fmt.Errorf("resolver %q is already provided by %s", name, previous.ID)
			}
		}
	}
	return checkPrerequisites(p, versions, schemas)
}

func checkPrerequisites(p Package, versions map[string]string, schemas map[string]int) error {
	for id, constraint := range p.Dependencies {
		version := versions[id]
		if version == "" || (constraint != "*" && constraint != version) {
			return fmt.Errorf("package %s requires %s at %s", p.ID, id, constraint)
		}
	}
	for _, id := range p.Members {
		if versions[id] == "" {
			return fmt.Errorf("install bundle member %s first", id)
		}
	}
	for id, supported := range p.LibrarySchemas {
		if version := schemas[id]; !supported.Supports(version) {
			return fmt.Errorf("package %s requires Library %s schema %d–%d, found %d", p.ID, id, supported.Min, supported.Max, version)
		}
	}
	return nil
}

// Exclude packages whose dependencies cannot run while the profile is damaged.
func availablePackages(installed []Package) []Package {
	available := slices.Clone(installed)
	for {
		versions := make(map[string]string, len(available))
		schemas := make(map[string]int, len(available))
		for _, p := range available {
			versions[p.ID] = p.Version
			if p.Library != nil {
				schemas[p.ID] = p.Library.SchemaVersion
			}
		}
		next := slices.DeleteFunc(available, func(p Package) bool {
			return checkPrerequisites(p, versions, schemas) != nil
		})
		if len(next) == len(available) {
			return next
		}
		available = next
	}
}

func (s *Store) Remove(selector string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	installed, damaged, err := s.Inventory()
	if err != nil {
		return err
	}
	if err := publishableDamages(damaged); err != nil {
		return err
	}
	p, err := findSelection(installed, damaged, selector)
	if err != nil {
		return err
	}
	for _, other := range installed {
		_, depends := other.Dependencies[p.ID]
		_, needsSchema := other.LibrarySchemas[p.ID]
		if depends || needsSchema {
			return fmt.Errorf("package %s is required by %s", p.ID, other.ID)
		}
		for _, id := range other.Members {
			if id == p.ID {
				return fmt.Errorf("package %s is a member of bundle %s", p.ID, other.ID)
			}
		}
	}
	remaining := make([]Package, 0, len(installed))
	for _, other := range installed {
		if other.ID != p.ID {
			remaining = append(remaining, other)
		}
	}
	if _, err := s.Environment(remaining); err != nil {
		return err
	}
	var retained []registryEntry
	if CanonicalType(p.Type) == "library" {
		if healthy, err := findPackage(installed, p.ID); err == nil {
			retained, err = s.publishPackages([]Package{healthy})
			if err != nil {
				return err
			}
		} else {
			for _, issue := range damaged {
				if issue.Package.ID == p.ID {
					retained = append(retained, issue.entry())
					break
				}
			}
		}
	}
	entries, err := s.publishSelections(remaining, damaged, map[string]bool{p.ID: true})
	if err != nil {
		return err
	}
	return s.writeRegistry(entries, nil, nil, true, retained...)
}

type copier struct {
	root  string
	bytes int64
	seen  map[string]bool
}

func (c *copier) reserve(name string) error {
	if err := validatePath(name); err != nil {
		return err
	}
	folded := strings.ToLower(name)
	if c.seen[folded] {
		return fmt.Errorf("duplicate package path %q", name)
	}
	if len(c.seen) == maxPackageFiles {
		return fmt.Errorf("package exceeds %d entries", maxPackageFiles)
	}
	c.seen[folded] = true
	return nil
}

func (c *copier) file(name string, mode fs.FileMode, size int64, source io.Reader) error {
	if size < 0 || size > maxPackageBytes-c.bytes {
		return fmt.Errorf("package exceeds %d bytes", maxPackageBytes)
	}
	path := filepath.Join(c.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644|mode&0o111)
	if err != nil {
		return err
	}
	count, copyErr := io.Copy(f, io.LimitReader(source, size+1))
	closeErr := errors.Join(f.Sync(), f.Close())
	if copyErr != nil {
		return copyErr
	}
	if count != size {
		return fmt.Errorf("package file %q changed size", name)
	}
	c.bytes += count
	return closeErr
}

func copyDirectory(source, target string) error {
	return copyPackageDirectory(source, target, false)
}

func copyPackageDirectory(source, target string, installed bool) error {
	c := copier{root: target, seen: make(map[string]bool)}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == source {
			return err
		}
		if strings.EqualFold(entry.Name(), ".git") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if installed && name == originFile {
			return nil
		}
		if err := c.reserve(name); err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(target, rel), 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported package file %q", name)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		return c.file(name, info.Mode(), info.Size(), file)
	})
}

func extractZIP(source, target string) error {
	archive, err := zip.OpenReader(source)
	if err != nil {
		if archive != nil {
			archive.Close()
		}
		return err
	}
	defer archive.Close()
	c := copier{root: target, seen: make(map[string]bool)}
	for _, entry := range archive.File {
		mode := entry.Mode()
		name := entry.Name
		if mode.IsDir() {
			name = strings.TrimSuffix(name, "/")
		}
		if err := c.reserve(name); err != nil {
			return err
		}
		if mode.IsDir() {
			if err := os.MkdirAll(filepath.Join(target, filepath.FromSlash(name)), 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() || entry.UncompressedSize64 > maxPackageBytes {
			return fmt.Errorf("unsupported ZIP entry %q", name)
		}
		file, err := entry.Open()
		if err != nil {
			return err
		}
		err = c.file(name, mode, int64(entry.UncompressedSize64), file)
		file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
