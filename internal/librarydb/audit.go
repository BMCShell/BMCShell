package librarydb

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
)

// AuditAppend is a committed audit line awaiting durable append to audit.jsonl.
type AuditAppend struct {
	Offset int64
	Line   []byte
}

func validAuditAppend(offset int64, line []byte) bool {
	return offset >= 0 && len(line) > 1 && line[len(line)-1] == '\n' &&
		bytes.IndexByte(line[:len(line)-1], '\n') < 0 && json.Valid(line)
}

func queueAudit(tx *sql.Tx, offset int64, line []byte) error {
	if !validAuditAppend(offset, line) {
		return errors.New("invalid pending Library audit entry")
	}
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS _bmcshell_library_pending_audit (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			audit_offset INTEGER NOT NULL CHECK (audit_offset >= 0),
			audit_line BLOB NOT NULL
		)
	`); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO _bmcshell_library_pending_audit VALUES (1, ?, ?)", offset, line)
	return err
}

// PendingAudit reads a working database through a writable connection so SQLite
// can recover a rollback journal after a previous process was interrupted.
func PendingAudit(path string) (*AuditAppend, error) {
	db, err := OpenReadWrite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var exists bool
	err = db.QueryRow("SELECT EXISTS (SELECT 1 FROM sqlite_schema WHERE name = ? AND type = 'table')", pendingAuditTable).Scan(&exists)
	if err != nil || !exists {
		return nil, err
	}
	var pending AuditAppend
	err = db.QueryRow("SELECT audit_offset, audit_line FROM _bmcshell_library_pending_audit WHERE id = 1").Scan(&pending.Offset, &pending.Line)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !validAuditAppend(pending.Offset, pending.Line) {
		return nil, errors.New("invalid pending Library audit entry")
	}
	return &pending, nil
}

func AcknowledgeAudit(path string, pending AuditAppend) error {
	db, err := OpenReadWrite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("DELETE FROM _bmcshell_library_pending_audit WHERE id = 1 AND audit_offset = ? AND audit_line = ?", pending.Offset, pending.Line)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("pending Library audit entry changed")
	}
	// Leave the normal working schema compatible with earlier portable cores.
	if _, err := tx.Exec("DROP TABLE _bmcshell_library_pending_audit"); err != nil {
		return err
	}
	return tx.Commit()
}
