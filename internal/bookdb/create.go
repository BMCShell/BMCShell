package bookdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Entry struct{ ID, Title, Body string }

const createSchema = `
CREATE TABLE book_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  schema_version INTEGER NOT NULL CHECK (schema_version = 1),
  title TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  content_version TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  data_table TEXT NOT NULL,
  list_fields TEXT NOT NULL,
  search_fields TEXT NOT NULL,
  detail_enabled INTEGER NOT NULL CHECK (detail_enabled IN (0,1)),
  lookup_keys TEXT NOT NULL,
  content_field TEXT,
  content_format TEXT NOT NULL DEFAULT 'plain',
  field_options TEXT NOT NULL DEFAULT '{}',
  order_by TEXT NOT NULL,
  help_text TEXT NOT NULL DEFAULT ''
);
CREATE TABLE entries (id INTEGER PRIMARY KEY, name TEXT NOT NULL, content TEXT NOT NULL DEFAULT '');
PRAGMA user_version = 1;
`

// Create writes a new 2.x-compatible Book database at an unused path.
func Create(path string, entries []Entry) (err error) {
	if !strings.HasSuffix(strings.ToLower(path), ".book.db") {
		return fmt.Errorf("Book data file must end in .book.db: %q", path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err := f.Close(); err != nil {
		return err
	}
	db, err := open(path, "rw")
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); err == nil {
			err = closeErr
		}
	}()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(createSchema); err != nil {
		return err
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".book.db")
	if !bookName.MatchString(name) {
		name = "book"
	}
	_, err = tx.Exec(`INSERT INTO book_meta (id, schema_version, title, name, description,
		content_version, updated_at, data_table, list_fields, search_fields, detail_enabled,
		lookup_keys, content_field, field_options, order_by)
		VALUES (1, 1, ?, ?, 'BMCShell 工具书', '1.0.0', ?, 'entries', '["id","name"]', '["name"]', 1,
		'[{"name":"id","option":"--id","fields":["id"]},{"name":"name","option":"--name","fields":["name"],"positional":true}]',
		'content', '{"id":{"title":"ID","format":"integer","width":6},"name":{"title":"名称","width":48}}', '["id"]')`,
		name, name, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, err := strconv.ParseInt(entry.ID, 10, 64)
		if err != nil || id < 0 || strings.TrimSpace(entry.Title) == "" || len(entry.Body) > bodyLimit {
			return fmt.Errorf("invalid Book entry %q", entry.ID)
		}
		if _, err := tx.Exec("INSERT INTO entries (id, name, content) VALUES (?, ?, ?)", id, entry.Title, entry.Body); err != nil {
			return err
		}
	}
	return tx.Commit()
}
