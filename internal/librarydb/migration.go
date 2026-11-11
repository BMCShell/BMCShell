package librarydb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

const maxMigrationBytes = 1 << 20

// Migrate applies trusted package SQL to a staged database. The framework owns the
// transaction and schema version, and the migration preserves the Library identity.
func Migrate(path, sqlPath string, fromVersion, toVersion int) error {
	if fromVersion < 1 || toVersion <= fromVersion || toVersion > math.MaxInt32 {
		return errors.New("Library migration needs increasing positive schema versions")
	}
	script, err := readMigration(sqlPath)
	if err != nil {
		return err
	}
	db, err := OpenReadWrite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRow(`SELECT id FROM main._bmcshell_library LIMIT 1`).Scan(&id); err != nil {
		return fmt.Errorf("Library migration identity: %w", err)
	}
	before, err := readIdentity(tx, id)
	if err != nil {
		return err
	}
	if before.SchemaVersion != fromVersion {
		return fmt.Errorf("Library migration needs version %d, found %d", fromVersion, before.SchemaVersion)
	}
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	if err := executeMigration(conn, script); err != nil {
		return fmt.Errorf("Library migration SQL: %w", err)
	}
	if _, err := tx.Exec(`UPDATE main._bmcshell_library SET schema_version = ?`, toVersion); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", toVersion)); err != nil {
		return err
	}
	after, err := readIdentity(tx, id)
	if err != nil {
		return err
	}
	if after.SchemaVersion != toVersion || after.DataVersion != before.DataVersion {
		return errors.New("Library migration changed protected metadata")
	}
	if err := quickCheck(tx); err != nil {
		return err
	}
	if err := checkForeignKeys(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func readMigration(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxMigrationBytes {
		return "", errors.New("Library migration must be a regular SQL file of at most 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMigrationBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxMigrationBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("Library migration must contain UTF-8 SQL of at most 1 MiB")
	}
	script := strings.TrimPrefix(string(data), "\ufeff")
	if strings.TrimSpace(script) == "" {
		return "", errors.New("Library migration SQL is empty")
	}
	return script, nil
}

func executeMigration(conn *sql.Conn, script string) error {
	return conn.Raw(func(value any) (err error) {
		driverConn, ok := value.(driver.Conn)
		if !ok {
			return errors.New("Library migration requires the SQLite driver")
		}
		sqlite := driverConn.Raw()
		if err := sqlite.SetAuthorizer(authorizeMigration); err != nil {
			return err
		}
		defer func() {
			if resetErr := sqlite.SetAuthorizer(nil); err == nil {
				err = resetErr
			}
		}()
		return sqlite.Exec(script)
	})
}

func authorizeMigration(action sqlite3.AuthorizerActionCode, first, second, _, _ string) sqlite3.AuthorizerReturnCode {
	switch action {
	case sqlite3.AUTH_ATTACH, sqlite3.AUTH_DETACH, sqlite3.AUTH_TRANSACTION, sqlite3.AUTH_SAVEPOINT, sqlite3.AUTH_PRAGMA,
		sqlite3.AUTH_CREATE_VTABLE, sqlite3.AUTH_DROP_VTABLE:
		return sqlite3.AUTH_DENY
	case sqlite3.AUTH_INSERT, sqlite3.AUTH_DELETE, sqlite3.AUTH_UPDATE, sqlite3.AUTH_DROP_TABLE:
		if strings.EqualFold(first, metadataTable) || strings.EqualFold(first, pendingAuditTable) {
			return sqlite3.AUTH_DENY
		}
	case sqlite3.AUTH_ALTER_TABLE, sqlite3.AUTH_CREATE_TRIGGER, sqlite3.AUTH_CREATE_TEMP_TRIGGER:
		if strings.EqualFold(second, metadataTable) || strings.EqualFold(second, pendingAuditTable) {
			return sqlite3.AUTH_DENY
		}
	case sqlite3.AUTH_FUNCTION:
		switch strings.ToLower(second) {
		case "load_extension", "readfile", "writefile":
			return sqlite3.AUTH_DENY
		}
	}
	return sqlite3.AUTH_OK
}

func checkForeignKeys(tx *sql.Tx) error {
	var table, parent string
	var row sql.NullInt64
	var key int
	err := tx.QueryRow("PRAGMA foreign_key_check").Scan(&table, &row, &parent, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Library foreign key check: %w", err)
	}
	return fmt.Errorf("Library foreign key check failed for table %q referencing %q", table, parent)
}
