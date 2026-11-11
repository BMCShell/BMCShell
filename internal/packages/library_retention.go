package packages

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"bmcshell/internal/librarydb"
)

type libraryHistoryReference struct {
	id, version, generation string
}

type libraryHistoryCandidate struct {
	libraryHistoryReference
	path string
}

// rememberLibraryPredecessor runs in the transaction that selects the new
// generation. Only positively identified predecessors enter the history.
func rememberLibraryPredecessor(tx *sql.Tx, activation LibraryActivation) error {
	if activation.PredecessorVersion == "" && activation.PredecessorGeneration == "" {
		return nil
	}
	version, generation := activation.PredecessorVersion, activation.PredecessorGeneration
	if version == activation.Version || ValidateVersion(version) != nil || !libraryGenerationName.MatchString(generation) {
		return errors.New("invalid Library history predecessor")
	}
	var current string
	err := tx.QueryRow("SELECT generation FROM library_generations WHERE id = ? AND version = ?",
		activation.ID, version).Scan(&current)
	if err != nil || current != generation {
		return errors.New("Library history predecessor changed; prepare the update again")
	}
	if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS library_upgrade_history (" +
		"id TEXT NOT NULL, version TEXT NOT NULL, generation TEXT NOT NULL, " +
		"latest INTEGER NOT NULL CHECK (latest IN (0, 1)), " +
		"PRIMARY KEY (id, generation)) WITHOUT ROWID"); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE library_upgrade_history SET latest = 0 WHERE id = ? AND latest = 1", activation.ID); err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO library_upgrade_history VALUES (?, ?, ?, 1) "+
		"ON CONFLICT (id, generation) DO UPDATE SET version = excluded.version, latest = 1",
		activation.ID, version, generation)
	return err
}

// PreviewLibraryRetention lists complete older snapshots whose audit is already
// present in the selected work, including eligible retired registrations.
func (s *Store) PreviewLibraryRetention() ([]string, error) {
	candidates, err := s.libraryHistoryCandidates(true)
	return libraryHistoryPaths(candidates), err
}

// PlanLibraryRetention lists only the paths safe to move without changing
// framework.db. It must be called on an offline profile.
func (s *Store) PlanLibraryRetention() ([]string, error) {
	candidates, err := s.libraryHistoryCandidates(false)
	return libraryHistoryPaths(candidates), err
}

func libraryHistoryPaths(candidates []libraryHistoryCandidate) []string {
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.path)
	}
	slices.Sort(paths)
	return paths
}

// RetireLibraryHistory forgets only superseded snapshot selections from the
// verified offline preview. The caller archives the now-unreferenced paths.
func (s *Store) RetireLibraryHistory(expected []string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	candidates, err := s.libraryHistoryCandidates(true)
	if err != nil {
		return err
	}
	if !slices.Equal(libraryHistoryPaths(candidates), expected) {
		return errors.New("Library history differs from the verified backup; preview it again")
	}
	var retired []libraryHistoryReference
	for _, candidate := range candidates {
		if candidate.version != "" {
			retired = append(retired, candidate.libraryHistoryReference)
		}
	}
	if len(retired) == 0 {
		return nil
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
	for _, ref := range retired {
		result, err := tx.Exec("DELETE FROM library_generations WHERE id = ? AND version = ? AND generation = ?",
			ref.id, ref.version, ref.generation)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("Library history registration changed; preview it again")
		}
	}
	return tx.Commit()
}

func (s *Store) libraryHistoryCandidates(includeRetired bool) ([]libraryHistoryCandidate, error) {
	installed, registered, err := s.registeredPackages()
	if err != nil || !registered {
		return nil, err
	}
	db, err := s.openMetadata("ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	selected, versions, err := s.libraryHistoryProtectedVersions(db, installed)
	if err != nil {
		return nil, err
	}
	var candidates []libraryHistoryCandidate
	for id, version := range selected {
		root, err := s.layoutDirectory([]string{"libraries", id}, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		generations, active, err := libraryHistoryGenerations(db, id, version)
		if err != nil {
			return nil, err
		}
		if active == "" || !completeLibraryHistory(filepath.Join(root, active), id) {
			continue
		}
		history, err := libraryHistoryPredecessors(db, id)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			generation := entry.Name()
			if !entry.IsDir() || !libraryGenerationName.MatchString(generation) || generation == active {
				continue
			}
			predecessor := history[generation]
			if predecessor.latest {
				continue
			}
			var retiredVersion string
			if refs := generations[generation]; len(refs) > 0 {
				if !includeRetired || len(refs) != 1 || predecessor.version != refs[0] || versions[id][refs[0]] {
					continue
				}
				retiredVersion = refs[0]
			}
			path := filepath.Join(root, generation)
			if completeLibraryHistory(path, id) &&
				libraryAuditPrefix(filepath.Join(path, "audit.jsonl"), filepath.Join(root, active, "audit.jsonl")) {
				candidates = append(candidates, libraryHistoryCandidate{
					libraryHistoryReference: libraryHistoryReference{id, retiredVersion, generation}, path: path,
				})
			}
		}
	}
	return candidates, nil
}

func (s *Store) libraryHistoryProtectedVersions(db *sql.DB, installed []registryEntry) (map[string]string, map[string]map[string]bool, error) {
	selected := make(map[string]string)
	protected := make(map[string]map[string]bool)
	protect := func(id, version string) error {
		if !packageID.MatchString(id) || ValidateVersion(version) != nil {
			return errors.New("invalid Library history release")
		}
		if protected[id] == nil {
			protected[id] = make(map[string]bool)
		}
		protected[id][version] = true
		return nil
	}
	for _, entry := range installed {
		if entry.Kind == "library" {
			if err := protect(entry.ID, entry.Version); err != nil {
				return nil, nil, err
			}
			selected[entry.ID] = entry.Version
		}
	}
	hasRetained, err := metadataHasTable(db, "last_library_release")
	if err != nil {
		return nil, nil, err
	}
	if hasRetained {
		rows, err := db.Query("SELECT id, version FROM last_library_release")
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id, version string
			if err := rows.Scan(&id, &version); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if err := protect(id, version); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if selected[id] == "" {
				selected[id] = version
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, nil, err
		}
	}
	hasRollback, err := metadataHasTable(db, "installed_package_rollback")
	if err != nil {
		return nil, nil, err
	}
	if hasRollback {
		rollback, err := s.readRegistryRollback()
		if err != nil {
			return nil, nil, err
		}
		if rollback != nil {
			for _, entry := range rollback.Previous {
				if entry.Kind == "library" {
					if err := protect(entry.ID, entry.Version); err != nil {
						return nil, nil, err
					}
				}
			}
		}
	}
	return selected, protected, nil
}

func libraryHistoryGenerations(db *sql.DB, id, activeVersion string) (map[string][]string, string, error) {
	hasGenerations, err := metadataHasTable(db, "library_generations")
	if err != nil || !hasGenerations {
		return nil, "", err
	}
	rows, err := db.Query("SELECT version, generation FROM library_generations WHERE id = ?", id)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	generations := make(map[string][]string)
	var active string
	for rows.Next() {
		var version, generation string
		if err := rows.Scan(&version, &generation); err != nil {
			return nil, "", err
		}
		if ValidateVersion(version) != nil || !libraryGenerationName.MatchString(generation) {
			return nil, "", errors.New("invalid registered Library history generation")
		}
		generations[generation] = append(generations[generation], version)
		if version == activeVersion {
			active = generation
		}
	}
	return generations, active, rows.Err()
}

type libraryHistoryPredecessor struct {
	version string
	latest  bool
}

func libraryHistoryPredecessors(db *sql.DB, id string) (map[string]libraryHistoryPredecessor, error) {
	hasHistory, err := metadataHasTable(db, "library_upgrade_history")
	if err != nil || !hasHistory {
		return nil, err
	}
	rows, err := db.Query("SELECT version, generation, latest FROM library_upgrade_history WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := make(map[string]libraryHistoryPredecessor)
	for rows.Next() {
		var generation string
		var predecessor libraryHistoryPredecessor
		if err := rows.Scan(&predecessor.version, &generation, &predecessor.latest); err != nil {
			return nil, err
		}
		if ValidateVersion(predecessor.version) != nil || !libraryGenerationName.MatchString(generation) {
			return nil, errors.New("invalid Library history predecessor")
		}
		history[generation] = predecessor
	}
	return history, rows.Err()
}

func completeLibraryHistory(path, id string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 3 {
		return false
	}
	for _, name := range []string{"work.library.db", "baseline.library.db", "audit.jsonl"} {
		path := filepath.Join(path, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		if name != "audit.jsonl" && !cleanLibraryHistoryDatabase(path, id) {
			return false
		}
	}
	return true
}

func cleanLibraryHistoryDatabase(path, id string) bool {
	db, err := librarydb.OpenReadOnly(path)
	if err != nil {
		return false
	}
	defer db.Close()
	var owner string
	if err := db.QueryRow("SELECT id FROM _bmcshell_library WHERE singleton = 1").Scan(&owner); err != nil || owner != id {
		return false
	}
	pending, err := metadataHasTable(db, "_bmcshell_library_pending_audit")
	if err != nil || pending {
		return false
	}
	var integrity string
	return db.QueryRow("PRAGMA quick_check(1)").Scan(&integrity) == nil && integrity == "ok"
}

func libraryAuditPrefix(olderPath, activePath string) bool {
	older, err := os.Open(olderPath)
	if err != nil {
		return false
	}
	defer older.Close()
	active, err := os.Open(activePath)
	if err != nil {
		return false
	}
	defer active.Close()
	oldInfo, err := older.Stat()
	if err != nil || oldInfo.Size() == 0 {
		return false
	}
	activeInfo, err := active.Stat()
	if err != nil || activeInfo.Size() <= oldInfo.Size() {
		return false
	}
	var end [1]byte
	if _, err := older.ReadAt(end[:], oldInfo.Size()-1); err != nil || end[0] != '\n' {
		return false
	}
	if _, err := active.ReadAt(end[:], activeInfo.Size()-1); err != nil || end[0] != '\n' {
		return false
	}
	oldDigest, activeDigest := sha256.New(), sha256.New()
	_, oldErr := io.CopyN(oldDigest, older, oldInfo.Size())
	_, activeErr := io.CopyN(activeDigest, active, oldInfo.Size())
	return oldErr == nil && activeErr == nil && bytes.Equal(oldDigest.Sum(nil), activeDigest.Sum(nil))
}
