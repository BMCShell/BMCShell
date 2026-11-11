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
		return errors.New("业务库迁移的结构版本须为正整数，目标版本须高于源版本")
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
		return fmt.Errorf("业务库迁移标识校验失败：%w", err)
	}
	before, err := readIdentity(tx, id)
	if err != nil {
		return err
	}
	if before.SchemaVersion != fromVersion {
		return fmt.Errorf("业务库迁移需要结构版本 %d，实际为 %d", fromVersion, before.SchemaVersion)
	}
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	if err := executeMigration(conn, script); err != nil {
		return fmt.Errorf("业务库迁移 SQL 执行失败：%w", err)
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
		return errors.New("业务库迁移更改了受保护的元数据")
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
		return "", errors.New("业务库迁移须使用不超过 1 MiB 的普通 SQL 文件")
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
		return "", errors.New("业务库迁移 SQL 须采用 UTF-8 编码且不超过 1 MiB")
	}
	script := strings.TrimPrefix(string(data), "\ufeff")
	if strings.TrimSpace(script) == "" {
		return "", errors.New("业务库迁移 SQL 为空")
	}
	return script, nil
}

func executeMigration(conn *sql.Conn, script string) error {
	return conn.Raw(func(value any) (err error) {
		driverConn, ok := value.(driver.Conn)
		if !ok {
			return errors.New("业务库迁移需要 SQLite 驱动")
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
		return fmt.Errorf("业务库外键检查失败：%w", err)
	}
	return fmt.Errorf("业务库外键检查失败，表 %q 引用了 %q", table, parent)
}
