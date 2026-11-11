package packages

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

type payloadID struct {
	ID, Kind, Version string
}

func (entry registryEntry) payloadID() payloadID {
	return payloadID{entry.ID, entry.Kind, entry.Version}
}

func hasPayloadRetention(kind string) bool {
	switch kind {
	case "book", "tool", "theme", "bundle":
		return true
	}
	return false
}

func (s *Store) recordSupersededPayloads(tx *sql.Tx, next []registryEntry) error {
	selected := make(map[payloadID]bool, len(next))
	for _, entry := range next {
		selected[entry.payloadID()] = true
	}

	rows, err := tx.Query("SELECT id, kind, version, source FROM installed_packages")
	if err != nil {
		return err
	}
	defer rows.Close()
	var superseded []registryEntry
	for rows.Next() {
		var entry registryEntry
		if err := rows.Scan(&entry.ID, &entry.Kind, &entry.Version, &entry.Source); err != nil {
			return err
		}
		if hasPayloadRetention(entry.Kind) && !selected[entry.payloadID()] {
			superseded = append(superseded, entry)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, entry := range superseded {
		p, err := s.loadRegisteredPackage(entry)
		if err != nil || p.validateDocumentation() != nil {
			// A damaged selection is not evidence that the payload was usable.
			continue
		}
		if _, err := tx.Exec("INSERT OR REPLACE INTO superseded_package_payloads (id, kind, version) VALUES (?, ?, ?)",
			entry.ID, entry.Kind, entry.Version); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) supersededPayloads() ([]registryEntry, bool, error) {
	db, err := s.openMetadata("ro")
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer db.Close()
	hasHistory, err := metadataHasTable(db, "superseded_package_payloads")
	if err != nil || !hasHistory {
		return nil, false, err
	}
	hasRollback, err := metadataHasTable(db, "installed_package_rollback")
	if err != nil {
		return nil, false, err
	}
	rows, err := db.Query("SELECT id, kind, version FROM superseded_package_payloads ORDER BY sequence DESC")
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var history []registryEntry
	for rows.Next() {
		var entry registryEntry
		if err := rows.Scan(&entry.ID, &entry.Kind, &entry.Version); err != nil {
			return nil, false, err
		}
		if !hasPayloadRetention(entry.Kind) || !packageID.MatchString(entry.ID) || ValidateVersion(entry.Version) != nil {
			return nil, false, fmt.Errorf("invalid superseded package identity %s %s %s", entry.Kind, entry.ID, entry.Version)
		}
		history = append(history, entry)
	}
	return history, hasRollback, rows.Err()
}

// PlanPayloadRetention lists superseded release directories safe to archive
// while the profile is offline. It never deletes payloads or changes metadata.
func (s *Store) PlanPayloadRetention() ([]string, error) {
	history, hasRollback, err := s.supersededPayloads()
	if err != nil || len(history) == 0 {
		return nil, err
	}
	active, registered, err := s.registeredPackages()
	if err != nil || !registered {
		return nil, err
	}
	type packageIdentity struct{ ID, Kind string }
	protected := make(map[payloadID]bool)
	activePayloads := make(map[payloadID]bool, len(active))
	kept := make(map[packageIdentity]int)
	var protectedPackages []Package
	for _, entry := range active {
		p, err := s.loadRegisteredPackage(entry)
		if err != nil {
			return nil, err
		}
		protected[entry.payloadID()] = true
		activePayloads[entry.payloadID()] = true
		kept[packageIdentity{entry.ID, entry.Kind}] = 1
		protectedPackages = append(protectedPackages, p)
	}
	if hasRollback {
		rollback, err := s.readRegistryRollback()
		if err != nil {
			return nil, err
		}
		if rollback != nil {
			for _, entry := range rollback.Previous {
				if protected[entry.payloadID()] {
					continue
				}
				p, err := s.loadRegisteredPackage(entry)
				if err != nil {
					return nil, err
				}
				protected[entry.payloadID()] = true
				protectedPackages = append(protectedPackages, p)
			}
		}
	}

	eligible := make(map[payloadID]Package)
	for _, entry := range history {
		id := entry.payloadID()
		if activePayloads[id] {
			continue
		}
		p, err := s.loadRegisteredPackage(entry)
		if err != nil || p.validateDocumentation() != nil {
			// Missing files may already be archived. Leave damaged data alone.
			continue
		}

		identity := packageIdentity{entry.ID, entry.Kind}
		if kept[identity] < 2 {
			kept[identity]++
			if !protected[id] {
				protected[id] = true
				protectedPackages = append(protectedPackages, p)
			}
		}
		if !protected[id] {
			eligible[id] = p
		}
	}

	// An exact dependency of any retained release pins that dependency and
	// its own requirements. Wildcard dependencies can use the retained
	// selected or predecessor release of the referenced package.
	byVersion := make(map[[2]string][]payloadID)
	for id := range eligible {
		key := [2]string{id.ID, id.Version}
		byVersion[key] = append(byVersion[key], id)
	}
	for i := 0; i < len(protectedPackages); i++ {
		for dep, version := range protectedPackages[i].Dependencies {
			for _, id := range byVersion[[2]string{dep, version}] {
				if p, ok := eligible[id]; ok {
					delete(eligible, id)
					protectedPackages = append(protectedPackages, p)
				}
			}
		}
	}

	var paths []string
	for _, p := range eligible {
		resolved, err := filepath.EvalSymlinks(p.Root)
		if err != nil {
			return nil, err
		}
		if resolved != p.Root && (runtime.GOOS != "windows" || !strings.EqualFold(resolved, p.Root)) {
			return nil, fmt.Errorf("superseded package path uses a link: %s", p.Root)
		}
		paths = append(paths, p.Root)
	}
	slices.Sort(paths)
	return paths, nil
}
