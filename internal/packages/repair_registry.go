package packages

import (
	"database/sql"
	"fmt"
)

const RepairFrameworkSource = "framework"

type RepairRecord struct {
	SourceID   string
	SourceName string
	Dependency string
	Suggestion string
	Platform   string
	Manager    string
	Package    string
}

// SyncRepairSuggestions rebuilds the private repair index from one installed
// package snapshot. The collector runs under the package mutation lock and
// should only read the supplied packages and built-in defaults.
func (s *Store) SyncRepairSuggestions(collect func([]Package) ([]RepairRecord, error)) ([]Package, []PackageDamage, []RepairRecord, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, nil, nil, err
	}
	defer unlock()

	installed, damaged, err := s.Inventory()
	if err != nil {
		return nil, nil, nil, err
	}
	suggestions, err := collect(installed)
	if err != nil {
		return nil, nil, nil, err
	}

	owners := map[string]string{RepairFrameworkSource: "BMCShell"}
	for _, p := range installed {
		owners[p.ID] = p.Name
	}

	db, err := s.openMetadata("rwc")
	if err != nil {
		return nil, nil, nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS repair_suggestions (
			source_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			dependency TEXT NOT NULL,
			suggestion TEXT NOT NULL,
			platform TEXT NOT NULL,
			manager TEXT NOT NULL,
			package_name TEXT NOT NULL,
			PRIMARY KEY (source_id, dependency, suggestion, platform, manager, package_name)
		) WITHOUT ROWID
	`); err != nil {
		return nil, nil, nil, err
	}
	if _, err := tx.Exec("DELETE FROM repair_suggestions"); err != nil {
		return nil, nil, nil, err
	}
	for _, item := range suggestions {
		owner, ok := owners[item.SourceID]
		if !ok {
			return nil, nil, nil, fmt.Errorf("unknown repair suggestion source %q", item.SourceID)
		}
		if _, err := tx.Exec(`
			INSERT INTO repair_suggestions (source_id, source_name, dependency, suggestion, platform, manager, package_name)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING
		`, item.SourceID, owner, item.Dependency, item.Suggestion, item.Platform, item.Manager, item.Package); err != nil {
			return nil, nil, nil, err
		}
	}
	index, err := readRepairSuggestions(tx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	return installed, damaged, index, nil
}

func readRepairSuggestions(tx *sql.Tx) ([]RepairRecord, error) {
	rows, err := tx.Query(`
		SELECT source_id, source_name, dependency, suggestion, platform, manager, package_name
		FROM repair_suggestions
		ORDER BY source_id != ?, source_name, source_id, dependency, suggestion, platform, manager, package_name
	`, RepairFrameworkSource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var suggestions []RepairRecord
	for rows.Next() {
		var item RepairRecord
		if err := rows.Scan(&item.SourceID, &item.SourceName, &item.Dependency, &item.Suggestion,
			&item.Platform, &item.Manager, &item.Package); err != nil {
			return nil, err
		}
		suggestions = append(suggestions, item)
	}
	return suggestions, rows.Err()
}
