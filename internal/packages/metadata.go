package packages

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
)

const activeStateLimit = 4096

func (s *Store) readActiveSet() (activeSet, error) {
	// Earlier cores selected the package generation through this marker.
	data, err := s.readCompatibilityMarker()
	if errors.Is(err, os.ErrNotExist) {
		data, err = s.readPackageState()
	}
	if err != nil || data == nil {
		return activeSet{}, err
	}
	if len(data) > activeStateLimit {
		return activeSet{}, errors.New("oversized active package-set record")
	}
	var set *activeSet
	if err := json.Unmarshal(data, &set); err != nil {
		return activeSet{}, fmt.Errorf("invalid active package-set record: %w", err)
	}
	if set == nil {
		return activeSet{}, errors.New("invalid active package-set record")
	}
	if set.Active != "" {
		if err := s.checkSetDirectory(set.Active); err != nil {
			return activeSet{}, err
		}
	}
	if set.Previous != nil {
		if *set.Previous == set.Active || !packageID.MatchString(set.Group) || set.Digest == "" ||
			set.PreviousDigest == "" || len(set.Checkpoint) > 256 {
			return activeSet{}, errors.New("invalid package rollback record")
		}
		if *set.Previous != "" && !setName.MatchString(*set.Previous) {
			return activeSet{}, errors.New("invalid previous package-set name")
		}
	}
	return *set, nil
}

func (s *Store) readCompatibilityMarker() ([]byte, error) {
	path := filepath.Join(s.dir, ".active-set")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > activeStateLimit {
		return nil, errors.New("invalid package-set marker")
	}
	return os.ReadFile(path)
}

func (s *Store) readPackageState() ([]byte, error) {
	db, err := s.openMetadata("ro")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer db.Close()
	initialized, err := metadataHasTable(db, "package_state")
	if err != nil || !initialized {
		return nil, err
	}
	var data []byte
	if err := db.QueryRow("SELECT payload FROM package_state WHERE id = 1").Scan(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Store) openMetadata(mode string) (*sql.DB, error) {
	path := filepath.Join(filepath.Dir(s.dir), "framework.db")
	info, err := os.Lstat(path)
	if err != nil && !(mode == "rwc" && errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("framework database must be a regular file")
	}
	uri := url.URL{Scheme: "file", Opaque: (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()}
	uri.RawQuery = url.Values{"mode": {mode}, "_pragma": {"trusted_schema(OFF)", "busy_timeout(5000)"}}.Encode()
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
