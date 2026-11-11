package cli

import (
	"errors"
	"fmt"
	"os"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

func auditSize(path string) (int64, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("业务库的审计文件不是普通文件")
	}
	return info.Size(), nil
}

// flushLibraryAudit must run under the Library lock. The expected offset keeps
// retries idempotent if the process stops before or after syncing the JSONL line.
func flushLibraryAudit(state libraryFiles) error {
	pending, err := librarydb.PendingAudit(state.work)
	if err != nil || pending == nil {
		return err
	}
	if err := writeAuditLine(state.audit, *pending); err != nil {
		return err
	}
	return librarydb.AcknowledgeAudit(state.work, *pending)
}

func writeAuditLine(path string, pending librarydb.AuditAppend) error {
	size, err := auditSize(path)
	if err != nil {
		return err
	}
	if size < pending.Offset || size-pending.Offset > int64(len(pending.Line)) {
		return errors.New("业务库的审计文件长度与工作库中的待同步记录不符")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.WriteAt(pending.Line, pending.Offset)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func (a *app) recoverLibraryAudit(p packages.Package, state libraryFiles) error {
	pending, err := librarydb.PendingAudit(state.work)
	if err != nil || pending == nil {
		return err
	}
	unlock, err := a.lockLibrary(p.ID)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := a.libraryFiles(p)
	if err != nil {
		return err
	}
	if current.registered != state.registered {
		return fmt.Errorf("业务库的工作库已切换，请重新打开")
	}
	return flushLibraryAudit(current)
}
