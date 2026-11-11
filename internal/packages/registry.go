package packages

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

type registryEntry struct {
	ID, Kind, Version, Source, Name string
}

type registryRollback struct {
	Group, Checkpoint, CurrentDigest, PreviousDigest string
	Previous                                         []registryEntry
}

// LibraryActivation selects a prepared generation for a package version.
type LibraryActivation struct {
	ID, Version, Previous, Generation         string
	PredecessorVersion, PredecessorGeneration string
}

var libraryGenerationName = regexp.MustCompile("^gen-[a-zA-Z0-9-]{1,96}$")

func (s *Store) payloadPath(entry registryEntry, createParents bool) (string, error) {
	if !packageID.MatchString(entry.ID) || ValidateVersion(entry.Version) != nil {
		return "", fmt.Errorf("已安装包的标识 %s %s 无效", entry.ID, entry.Version)
	}
	var parts []string
	switch entry.Kind {
	case "book":
		parts = []string{"books", entry.ID}
	case "library":
		parts = []string{"libraries", entry.ID, "releases"}
	case "tool", "theme", "bundle":
		parts = []string{"plugins", entry.Kind, entry.ID}
	default:
		return "", fmt.Errorf("已安装的包类型 %q 无效", entry.Kind)
	}
	parent, err := s.layoutDirectory(parts, createParents)
	if err != nil {
		return filepath.Join(filepath.Dir(s.dir), filepath.Join(parts...), entry.Version), err
	}
	return filepath.Join(parent, entry.Version), nil
}

func (s *Store) layoutDirectory(parts []string, create bool) (string, error) {
	path := filepath.Dir(s.dir)
	for _, part := range parts {
		path = filepath.Join(path, part)
		if create {
			if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("安装存储目录 %q 无效", path)
		}
	}
	return path, nil
}

func (s *Store) registeredPackages() ([]registryEntry, bool, error) {
	db, err := s.openMetadata("ro")
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer db.Close()
	exists, err := metadataHasTable(db, "installed_packages")
	if err != nil || !exists {
		return nil, false, err
	}
	query := "SELECT id, kind, version, source, '' FROM installed_packages ORDER BY id"
	names, err := metadataHasTable(db, "installed_package_names")
	if err != nil {
		return nil, false, err
	}
	if names {
		query = `SELECT p.id, p.kind, p.version, p.source, COALESCE(n.name, '')
			FROM installed_packages p LEFT JOIN installed_package_names n ON n.id = p.id
			ORDER BY p.id`
	}
	rows, err := db.Query(query)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var entries []registryEntry
	for rows.Next() {
		var entry registryEntry
		if err := rows.Scan(&entry.ID, &entry.Kind, &entry.Version, &entry.Source, &entry.Name); err != nil {
			return nil, false, err
		}
		entries = append(entries, entry)
	}
	return entries, true, rows.Err()
}

func metadataHasTable(db *sql.DB, table string) (bool, error) {
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?)", table).Scan(&exists)
	return exists, err
}

func (s *Store) loadRegisteredPackages(entries []registryEntry) ([]Package, error) {
	var installed []Package
	for _, entry := range entries {
		p, err := s.loadRegisteredPackage(entry)
		if err != nil {
			return nil, err
		}
		installed = append(installed, p)
	}
	return installed, nil
}

func (s *Store) loadRegisteredPackage(entry registryEntry) (Package, error) {
	fallback := Package{
		Manifest: Manifest{ID: entry.ID, Type: entry.Kind, Name: entry.Name, Version: entry.Version},
		Source:   entry.Source,
	}
	path, err := s.payloadPath(entry, false)
	fallback.Root = path
	if err != nil {
		return fallback, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fallback, err
	}
	if !info.IsDir() {
		return fallback, fmt.Errorf("包内容目录 %q 无效", path)
	}
	p, err := loadPackage(path)
	if err != nil {
		return fallback, fmt.Errorf("已安装的包 %s：%w", entry.ID, err)
	}
	if p.ID != entry.ID || CanonicalType(p.Type) != entry.Kind || p.Version != entry.Version {
		return fallback, fmt.Errorf("已安装的包 %s 的清单与登记记录不一致", entry.ID)
	}
	if entry.Source != "" && !originName.MatchString(entry.Source) {
		return fallback, fmt.Errorf("已安装的包 %s 的来源无效", entry.ID)
	}
	if _, err := p.RepairRecords(); err != nil {
		return fallback, err
	}
	p.Source = entry.Source
	return p, nil
}

// publishPackages prepares each immutable payload directory before registry
// activation. Previously published directories stay available to readers and
// are reused only if the requested release has identical contents.
func (s *Store) publishPackages(list []Package) ([]registryEntry, error) {
	return s.publishSelections(list, nil, nil)
}

func (s *Store) publishSelections(list []Package, damaged []PackageDamage, repaired map[string]bool) ([]registryEntry, error) {
	entries := make([]registryEntry, 0, len(list)+len(damaged))
	for _, p := range list {
		entry := registryEntry{ID: p.ID, Kind: CanonicalType(p.Type), Version: p.Version, Source: p.Source, Name: p.Name}
		root, err := s.payloadPath(entry, true)
		if err != nil {
			return nil, err
		}
		if p.Root != root {
			publish := publishPayload
			if repaired[p.ID] {
				publish = repairPayload
			}
			if err := publish(p, root); err != nil {
				return nil, err
			}
		}
		entries = append(entries, entry)
	}
	for _, issue := range damaged {
		if !repaired[issue.Package.ID] {
			entries = append(entries, issue.entry())
		}
	}
	return entries, nil
}

func repairSelectedRelease(p Package, damaged []PackageDamage) (bool, error) {
	for _, issue := range damaged {
		current := issue.Package
		if current.ID != p.ID {
			continue
		}
		if current.Type != CanonicalType(p.Type) || current.Version != p.Version {
			return false, fmt.Errorf("请先按已登记的类型和版本修复 %s（%s %s）", p.ID, current.Type, current.Version)
		}
		return true, nil
	}
	return false, nil
}

func sameDamagedSelections(current, expected []PackageDamage) bool {
	return slices.EqualFunc(current, expected, func(a, b PackageDamage) bool {
		return a.entry() == b.entry() && a.legacy == b.legacy
	})
}

// repairPayload replaces a diagnosed damaged release only after the incoming
// files are ready. The displaced release remains available for inspection.
func repairPayload(p Package, target string) error {
	parent := filepath.Dir(target)
	stage, err := os.MkdirTemp(parent, ".payload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := copyPackageDirectory(p.Root, stage, true); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(parent, ".damaged-")
	if err != nil {
		return err
	}
	displaced := filepath.Join(backup, "release")
	moved := false
	defer func() {
		if !moved {
			_ = os.Remove(backup)
		}
	}()
	if err := os.Rename(target, displaced); err == nil {
		moved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		if moved {
			restoreErr := os.Rename(displaced, target)
			moved = restoreErr != nil
			return errors.Join(err, restoreErr)
		}
		return err
	}
	return nil
}

func publishPayload(p Package, target string) error {
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() {
			return repairPayload(p, target)
		}
		previous, err := loadPackage(target)
		if err != nil || previous.ID != p.ID || CanonicalType(previous.Type) != CanonicalType(p.Type) ||
			previous.Version != p.Version {
			return repairPayload(p, target)
		}
		if _, err := previous.RepairRecords(); err != nil {
			return repairPayload(p, target)
		}
		wanted := p
		wanted.Source = ""
		existing := wanted
		existing.Root = target
		first, err := packageSetDigest([]Package{wanted})
		if err != nil {
			return err
		}
		second, err := packageSetDigest([]Package{existing})
		if err != nil {
			return err
		}
		if first != second {
			return fmt.Errorf("包 %s %s 的内容有变更，请发布新版本", p.Name, p.Version)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".payload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := copyPackageDirectory(p.Root, stage, true); err != nil {
		return err
	}
	return os.Rename(stage, target)
}

func (s *Store) writeRegistry(entries []registryEntry, activations []LibraryActivation, rollback *registryRollback, preserveRollback bool, retained ...registryEntry) error {
	for _, activation := range activations {
		if err := s.validateLibraryActivation(activation); err != nil {
			return err
		}
	}
	db, err := s.openMetadata("rwc")
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS installed_packages (
			id TEXT PRIMARY KEY, kind TEXT NOT NULL, version TEXT NOT NULL, source TEXT NOT NULL
		) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS library_generations (
			id TEXT NOT NULL, version TEXT NOT NULL, generation TEXT NOT NULL,
			PRIMARY KEY(id, version)
		) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS superseded_package_payloads (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL, kind TEXT NOT NULL, version TEXT NOT NULL,
			UNIQUE(id, kind, version)
		);
		CREATE TABLE IF NOT EXISTS installed_package_names (
			id TEXT PRIMARY KEY, name TEXT NOT NULL
		) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS last_library_release (
			id TEXT PRIMARY KEY, version TEXT NOT NULL, source TEXT NOT NULL, name TEXT NOT NULL
		) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS installed_package_rollback (
			id INTEGER PRIMARY KEY CHECK(id = 1), payload BLOB NOT NULL
		)
	`); err != nil {
		return err
	}
	if err := s.recordSupersededPayloads(tx, entries); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM installed_packages; DELETE FROM installed_package_names"); err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := tx.Exec("INSERT INTO installed_packages VALUES (?, ?, ?, ?)", entry.ID, entry.Kind, entry.Version, entry.Source); err != nil {
			return err
		}
		if entry.Name != "" {
			if _, err := tx.Exec("INSERT INTO installed_package_names VALUES (?, ?)", entry.ID, entry.Name); err != nil {
				return err
			}
		}
	}
	for _, entry := range append(slices.Clone(entries), retained...) {
		if entry.Kind == "library" {
			if _, err := tx.Exec("INSERT OR REPLACE INTO last_library_release VALUES (?, ?, ?, ?)", entry.ID, entry.Version, entry.Source, entry.Name); err != nil {
				return err
			}
		}
	}
	for _, activation := range activations {
		if err := selectLibraryGeneration(tx, activation); err != nil {
			return err
		}
		if err := rememberLibraryPredecessor(tx, activation); err != nil {
			return err
		}
	}
	if !preserveRollback {
		if _, err := tx.Exec("DELETE FROM installed_package_rollback"); err != nil {
			return err
		}
	}
	if rollback != nil {
		data, err := json.Marshal(rollback)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO installed_package_rollback VALUES (1, ?)", data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) readRegistryRollback() (*registryRollback, error) {
	db, err := s.openMetadata("ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var data []byte
	err = db.QueryRow("SELECT payload FROM installed_package_rollback WHERE id = 1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rollback registryRollback
	if err := json.Unmarshal(data, &rollback); err != nil {
		return nil, fmt.Errorf("包回滚记录无效：%w", err)
	}
	return &rollback, nil
}

func (s *Store) LibraryGeneration(id, version string) (string, error) {
	if !packageID.MatchString(id) || ValidateVersion(version) != nil {
		return "", fmt.Errorf("业务库标识 %s %s 无效", id, version)
	}
	db, err := s.openMetadata("ro")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer db.Close()
	exists, err := metadataHasTable(db, "library_generations")
	if err != nil || !exists {
		return "", err
	}
	var selected string
	err = db.QueryRow("SELECT generation FROM library_generations WHERE id = ? AND version = ?", id, version).Scan(&selected)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !libraryGenerationName.MatchString(selected) {
		return "", errors.New("已登记的业务库数据版本无效")
	}
	return selected, nil
}

// SetLibraryGeneration compares the expected selection and activates a prepared
// generation only while its Library package version is still installed.
func (s *Store) SetLibraryGeneration(id, version, expected, next string) error {
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
	if len(damaged) != 0 {
		installed = availablePackages(installed)
	}
	activation := LibraryActivation{ID: id, Version: version, Previous: expected, Generation: next}
	if err := validateLibrarySelections(installed, []LibraryActivation{activation}); err != nil {
		return err
	}
	entries, registered, err := s.registeredPackages()
	if err != nil {
		return err
	}
	if !registered {
		entries, err = s.publishPackages(installed)
		if err != nil {
			return err
		}
		return s.writeRegistry(entries, []LibraryActivation{activation}, nil, true)
	}
	if err := s.validateLibraryActivation(activation); err != nil {
		return err
	}
	db, err := s.openMetadata("rw")
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := selectLibraryGeneration(tx, activation); err != nil {
		return err
	}
	return tx.Commit()
}

func validateLibrarySelections(list []Package, activations []LibraryActivation) error {
	seen := make(map[string]bool)
	for _, activation := range activations {
		p, err := findPackage(list, activation.ID)
		if err != nil || p.ID != activation.ID || p.Version != activation.Version || p.Library == nil {
			return fmt.Errorf("业务库 %s %s 已不再选用", activation.ID, activation.Version)
		}
		if seen[activation.ID] {
			return fmt.Errorf("业务库 %s 被重复选中", activation.ID)
		}
		seen[activation.ID] = true
	}
	return nil
}

func (s *Store) validateLibraryActivation(activation LibraryActivation) error {
	if !packageID.MatchString(activation.ID) || ValidateVersion(activation.Version) != nil ||
		!libraryGenerationName.MatchString(activation.Generation) {
		return errors.New("选择的业务库数据版本无效")
	}
	_, err := s.layoutDirectory([]string{"libraries", activation.ID, activation.Generation}, false)
	return err
}

func selectLibraryGeneration(tx *sql.Tx, activation LibraryActivation) error {
	var current string
	err := tx.QueryRow("SELECT generation FROM library_generations WHERE id = ? AND version = ?",
		activation.ID, activation.Version).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != activation.Previous {
		return fmt.Errorf("业务库 %s 的当前数据版本发生变更，请重新准备更新", activation.ID)
	}
	_, err = tx.Exec(`
		INSERT INTO library_generations VALUES (?, ?, ?)
		ON CONFLICT(id, version) DO UPDATE SET generation = excluded.generation
	`, activation.ID, activation.Version, activation.Generation)
	return err
}
