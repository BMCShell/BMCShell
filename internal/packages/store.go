package packages

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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

func (s *Store) List() ([]Package, error) {
	set, err := s.readActiveSet()
	if err != nil {
		return nil, err
	}
	return listSet(s.setPath(set.Active))
}

func listSet(root string) ([]Package, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var installed []Package
	for _, entry := range entries {
		if !packageID.MatchString(entry.Name()) {
			continue
		}
		if !entry.IsDir() {
			return nil, fmt.Errorf("invalid package directory %s", entry.Name())
		}
		p, err := loadPackage(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("installed package %s: %w", entry.Name(), err)
		}
		if p.ID != entry.Name() {
			return nil, fmt.Errorf("installed package ID %s does not match directory %s", p.ID, entry.Name())
		}
		if p.Source, err = readSource(p.Root); err != nil {
			return nil, fmt.Errorf("installed package %s: %w", entry.Name(), err)
		}
		installed = append(installed, p)
	}
	return installed, nil
}

func (s *Store) Find(selector string) (Package, error) {
	installed, err := s.List()
	if err != nil {
		return Package{}, err
	}
	return findPackage(installed, selector)
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
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, ".mutation.lock")
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("package store is busy: %s", path)
		}
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

// Install copies a local directory or ZIP into staging, then publishes it by rename.
func (s *Store) Install(source string) (Package, error) {
	unlock, err := s.lock()
	if err != nil {
		return Package{}, err
	}
	defer unlock()
	stage, err := os.MkdirTemp(s.dir, ".install-")
	if err != nil {
		return Package{}, err
	}
	defer os.RemoveAll(stage)
	p, err := s.stagePackage(source, stage)
	if err != nil {
		return Package{}, err
	}
	set, err := s.readActiveSet()
	if err != nil {
		return Package{}, err
	}
	active := s.setPath(set.Active)
	installed, err := listSet(active)
	if err != nil {
		return Package{}, err
	}
	if err := checkInstall(p, installed); err != nil {
		return Package{}, err
	}
	if _, err := s.Environment(append(installed, p)); err != nil {
		return Package{}, err
	}
	destination := filepath.Join(active, p.ID)
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return Package{}, err
		}
		return Package{}, fmt.Errorf("package %s already exists", p.ID)
	}
	if err := os.Rename(stage, destination); err != nil {
		return Package{}, err
	}
	p.Root = destination
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

func (s *Store) Remove(selector string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	installed, err := s.List()
	if err != nil {
		return err
	}
	p, err := findPackage(installed, selector)
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
	remaining := make([]Package, 0, len(installed)-1)
	for _, other := range installed {
		if other.ID != p.ID {
			remaining = append(remaining, other)
		}
	}
	if _, err := s.Environment(remaining); err != nil {
		return err
	}
	trash, err := os.MkdirTemp(s.dir, ".remove-")
	if err != nil {
		return err
	}
	if err := os.Rename(p.Root, filepath.Join(trash, p.ID)); err != nil {
		_ = os.Remove(trash)
		return err
	}
	return os.RemoveAll(trash)
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
	closeErr := f.Close()
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
