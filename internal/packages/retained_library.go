package packages

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
)

type AmbiguousRetainedLibraryError struct{ ID string }

func (e *AmbiguousRetainedLibraryError) Error() string {
	return fmt.Sprintf("Library %s has several retained versions and no recorded last release; reinstall the last version used before changing versions", e.ID)
}

// RetainedLibrary returns the last active release, including a removed package.
// If its release files are damaged, it returns the known identity and an error.
func (s *Store) RetainedLibrary(id string) (Package, error) {
	if !packageID.MatchString(id) {
		return Package{}, fmt.Errorf("invalid Library ID %q", id)
	}
	entries, _, err := s.registeredPackages()
	if err != nil {
		return Package{}, err
	}
	for _, entry := range entries {
		if entry.ID == id && entry.Kind == "library" {
			return s.loadRegisteredPackage(entry)
		}
	}
	db, err := s.openMetadata("ro")
	if errors.Is(err, os.ErrNotExist) {
		return Package{}, nil
	}
	if err != nil {
		return Package{}, err
	}
	defer db.Close()
	entry := registryEntry{ID: id, Kind: "library"}
	hasHistory, err := metadataHasTable(db, "last_library_release")
	if err != nil {
		return Package{}, err
	}
	if hasHistory {
		err := db.QueryRow("SELECT version, source, name FROM last_library_release WHERE id = ?", id).
			Scan(&entry.Version, &entry.Source, &entry.Name)
		if err == nil {
			return s.loadRegisteredPackage(entry)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Package{}, err
		}
	}
	hasGenerations, err := metadataHasTable(db, "library_generations")
	if err != nil || !hasGenerations {
		return Package{}, err
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*), COALESCE(MIN(version), '') FROM library_generations WHERE id = ?", id).
		Scan(&count, &entry.Version); err != nil {
		return Package{}, err
	}
	switch count {
	case 0:
		return Package{}, nil
	case 1:
		return s.loadRegisteredPackage(entry)
	default:
		return Package{}, &AmbiguousRetainedLibraryError{ID: id}
	}
}
