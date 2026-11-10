package librarydb

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// OpenReadOnly opens an existing Library file without enabling schema-defined functions.
func OpenReadOnly(path string) (*sql.DB, error) {
	return open(path, "ro", false)
}

// OpenReadWrite opens an existing Library file with foreign key checks enabled.
func OpenReadWrite(path string) (*sql.DB, error) {
	return open(path, "rw", false)
}

func open(path, mode string, immutable bool) (*sql.DB, error) {
	file, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !file.Mode().IsRegular() {
		return nil, fmt.Errorf("Library database must be a regular file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Opaque: (&url.URL{Path: filepath.ToSlash(abs)}).EscapedPath()}
	params := uri.Query()
	params.Set("mode", mode)
	if immutable {
		params.Set("immutable", "1")
	}
	params.Add("_pragma", "trusted_schema(OFF)")
	params.Add("_pragma", "busy_timeout(1000)")
	if mode == "ro" {
		params.Add("_pragma", "query_only(ON)")
	} else {
		params.Add("_pragma", "foreign_keys(ON)")
	}
	uri.RawQuery = params.Encode()
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func queryRows[T any](tx *sql.Tx, scan func(*sql.Rows) (T, error), query string, args ...any) ([]T, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []T
	for rows.Next() {
		value, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
