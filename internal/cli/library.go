package cli

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bmcshell/internal/filelock"
	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

const maxLibrarySnapshot = 128 << 20

type libraryFiles struct {
	root, version, registered, generation string
	work, baseline, audit                 string
	fingerprint                           [sha256.Size]byte
}

func (a *app) libraryFiles(p packages.Package) (libraryFiles, error) {
	state := libraryFiles{root: filepath.Join(a.home, "libraries", p.ID), version: p.Version}
	registered, err := a.store.LibraryGeneration(p.ID, p.Version)
	if err != nil {
		return state, err
	}
	state.registered = registered
	if registered != "" {
		state.generation = filepath.Join(state.root, registered)
	} else {
		state.generation, err = legacyLibraryGeneration(state.root, state.version)
		if err != nil {
			return state, err
		}
	}
	if state.generation == "" {
		state.work, err = p.File(p.Data)
		state.baseline = state.work
	} else {
		state.work = filepath.Join(state.generation, "work.library.db")
		state.baseline = filepath.Join(state.generation, "baseline.library.db")
		state.audit = filepath.Join(state.generation, "audit.jsonl")
	}
	return state, err
}

func legacyLibraryGeneration(root, version string) (string, error) {
	legacyRoot := filepath.Join(root, version)
	marker := filepath.Join(legacyRoot, "active")
	info, err := os.Lstat(marker)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 256 {
		return "", errors.New("invalid legacy Library generation marker")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return "", err
	}
	active := strings.TrimSpace(string(data))
	if !strings.HasPrefix(active, "gen-") || filepath.Base(active) != active ||
		strings.ContainsAny(active, "/\\:") {
		return "", errors.New("invalid legacy Library generation")
	}
	return filepath.Join(legacyRoot, active), nil
}

func (a *app) importLegacyLibrary(p packages.Package) (libraryFiles, error) {
	unlock, err := a.lockLibrary(p.ID)
	if err != nil {
		return libraryFiles{}, err
	}
	defer unlock()

	return a.importLegacyLibraryLocked(p)
}

func (a *app) importLegacyLibraryLocked(p packages.Package) (libraryFiles, error) {
	state, err := a.libraryFiles(p)
	if err != nil || state.registered != "" || state.generation == "" {
		return state, err
	}
	state.fingerprint, err = libraryFingerprint(state)
	if err != nil {
		return state, err
	}
	directory, err := newLibraryGeneration(state.root)
	if err != nil {
		return state, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(directory)
		}
	}()
	if err := copyRetainedLibrary(state, directory, p); err != nil {
		return state, err
	}
	if err := checkLibraryGeneration(state); err != nil {
		return state, err
	}
	if err := a.store.SetLibraryGeneration(p.ID, p.Version, "", filepath.Base(directory)); err != nil {
		return state, err
	}
	published = true
	return a.libraryFiles(p)
}

func newLibraryGeneration(root string) (string, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, "gen-")
}

// copyRetainedLibrary imports a legacy version without changing its saved files.
func copyRetainedLibrary(state libraryFiles, directory string, p packages.Package) error {
	work := filepath.Join(directory, "work.library.db")
	baseline := filepath.Join(directory, "baseline.library.db")
	if err := copyLibraryFile(state.work, work); err != nil {
		return err
	}
	if err := copyLibraryFile(state.baseline, baseline); err != nil {
		return err
	}
	for _, file := range []string{work, baseline} {
		if _, err := librarydb.ValidateWorking(file, p.ID, *p.Library); err != nil {
			return err
		}
	}
	audit := filepath.Join(directory, "audit.jsonl")
	if err := copyLibraryAudit(state.audit, audit); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return copyLibrarySnapshot(audit, strings.NewReader(""), 0)
}

func (a *app) libraryInfo(p packages.Package) error {
	fmt.Fprintf(a.out, "%s %s\n", p.DataName(), p.Version)
	if p.Library == nil {
		fmt.Fprintln(a.out, "旧版 JSON 数据")
		return nil
	}
	state, err := a.libraryFiles(p)
	if err != nil {
		return err
	}
	if state.registered == "" && state.generation != "" {
		state, err = a.importLegacyLibrary(p)
		if err != nil {
			return err
		}
	}
	if state.registered != "" {
		if err := a.recoverLibraryAudit(p, state); err != nil {
			fmt.Fprintf(a.errOut, "Library 审计待同步：%v\n", err)
		}
	}
	snapshot, err := librarydb.ValidateWorking(state.work, p.ID, *p.Library)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "结构版本 %d，数据版本 %s\n", snapshot.SchemaVersion, snapshot.DataVersion)
	for _, table := range p.Library.Tables {
		fmt.Fprintf(a.out, "%s\t键 %s\n", table.Name, strings.Join(table.Keys, ", "))
	}
	if terminalInput(a.in) {
		return a.libraryMenu(p, bufio.NewReader(a.in))
	}
	return nil
}

func copyLibrarySnapshot(path string, source io.Reader, limit int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if limit != 0 {
		source = io.LimitReader(source, limit+1)
	}
	size, err := io.Copy(f, source)
	if err != nil {
		return err
	}
	if limit != 0 && size > limit {
		return errors.New("Library 快照超过 128 MiB")
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func copyLibraryFile(from, to string) error {
	return copyLibraryFileWithLimit(from, to, maxLibrarySnapshot)
}

func copyLibraryAudit(from, to string) error {
	return copyLibraryFileWithLimit(from, to, 0)
}

func copyLibraryFileWithLimit(from, to string, limit int64) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Library 文件不是普通文件: %s", from)
	}
	file, err := os.Open(from)
	if err != nil {
		return err
	}
	defer file.Close()
	return copyLibrarySnapshot(to, file, limit)
}

func readChoice(in *bufio.Reader) string {
	line, _ := in.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(line))
}

func (a *app) lockLibrary(id string) (func(), error) {
	root := filepath.Join(a.home, "libraries", id)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	unlock, err := filelock.Acquire(filepath.Join(root, ".sync.lock"))
	if err != nil {
		return nil, fmt.Errorf("无法锁定 Library 同步: %w", err)
	}
	return unlock, nil
}

func checkLibraryGeneration(state libraryFiles) error {
	if state.registered == "" {
		generation, err := legacyLibraryGeneration(state.root, state.version)
		if err != nil {
			return err
		}
		if generation != state.generation {
			return errors.New("确认期间 Library 已更新，请重新预览")
		}
	}
	fingerprint, err := libraryFingerprint(state)
	if err != nil {
		return err
	}
	if fingerprint != state.fingerprint {
		return errors.New("确认期间 Library 文件发生修改，请重新预览")
	}
	return nil
}

func libraryFingerprint(state libraryFiles) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	hash := sha256.New()
	for i, path := range []string{state.work, state.baseline} {
		if path == "" || i != 0 && path == state.work {
			continue
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				return result, errors.New("Library 有 SQLite 附属文件，请先结束本地编辑并保存独立快照")
			}
		}
		file, err := os.Open(path)
		if err != nil {
			return result, err
		}
		n, err := io.Copy(hash, io.LimitReader(file, maxLibrarySnapshot+1))
		closeErr := file.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return result, err
		}
		if n > maxLibrarySnapshot {
			return result, errors.New("Library 快照超过 128 MiB")
		}
	}
	hash.Sum(result[:0])
	return result, nil
}
