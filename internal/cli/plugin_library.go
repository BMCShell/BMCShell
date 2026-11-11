package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

type catalogLibraryState struct {
	before     *libraryFiles
	directory  string
	activation packages.LibraryActivation
}

type libraryAuditEntry struct {
	Time    time.Time            `json:"time"`
	Source  string               `json:"source"`
	Action  string               `json:"action"`
	Version string               `json:"version"`
	Changes []libraryAuditChange `json:"changes,omitempty"`
}

type libraryAuditChange struct {
	Table    string `json:"table"`
	Key      []any  `json:"key"`
	Field    string `json:"field,omitempty"`
	Action   string `json:"action"`
	Conflict bool   `json:"conflict,omitempty"`
	Selected string `json:"selected"`
}

func (a *app) installLocalPackages(paths []string, input *bufio.Reader, interactive bool) error {
	installed, err := a.store.List()
	if err != nil {
		return err
	}
	plan, err := a.store.PrepareChanges(paths, installed)
	if err != nil {
		return err
	}
	defer plan.Close()
	// Upgrade plans can skip an incompatible Library. An install must apply the whole batch.
	if err := errors.Join(plan.SkippedLibraryUpdates()...); err != nil {
		return err
	}

	changed := plan.ChangedPackages()
	if len(changed) == 0 {
		fmt.Fprintln(a.out, "已安装相同版本，保留现有工作库。")
		return nil
	}
	if err := a.commitCatalogPlan(plan, input, interactive); err != nil {
		return err
	}
	for _, p := range changed {
		fmt.Fprintf(a.out, "已安装 %s %s\n", p.Name, p.Version)
	}
	return nil
}

// commitCatalogPlan stages Library work, baseline, and audit before publishing
// the package set. Readers of the current version keep using the previous work.
func (a *app) commitCatalogPlan(plan *packages.Upgrade, input *bufio.Reader, interactive bool) (err error) {
	for _, skipped := range plan.SkippedLibraryUpdates() {
		fmt.Fprintln(a.errOut, skipped)
	}
	changed := plan.ChangedPackages()
	if len(changed) == 0 {
		return nil
	}
	installed := make(map[string]packages.Package)
	for _, p := range plan.OriginalPackages() {
		installed[p.ID] = p
	}
	var states []*catalogLibraryState
	var locks []func()
	defer func() {
		if err != nil {
			for i := len(states) - 1; i >= 0; i-- {
				err = errors.Join(err, os.RemoveAll(states[i].directory))
			}
		}
		for _, unlock := range locks {
			unlock()
		}
	}()
	for _, next := range changed {
		if next.Library == nil {
			continue
		}
		unlock, err := a.lockLibrary(next.ID)
		if err != nil {
			return err
		}
		locks = append(locks, unlock)
		state, err := a.stageCatalogLibrary(installed[next.ID], next, input, interactive)
		if state != nil {
			states = append(states, state)
		}
		if err != nil {
			return err
		}
	}
	activations := make([]packages.LibraryActivation, 0, len(states))
	for _, state := range states {
		activations = append(activations, state.activation)
	}
	return a.store.CommitChangesWithLibraries(plan, activations, func() error {
		for _, state := range states {
			if state.before != nil {
				if err := checkLibraryGeneration(*state.before); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (a *app) stageCatalogLibrary(old, next packages.Package, input *bufio.Reader, interactive bool) (*catalogLibraryState, error) {
	targetFiles, err := a.libraryFiles(next)
	if err != nil {
		return nil, err
	}
	incoming, err := next.File(next.Data)
	if err != nil {
		return nil, err
	}
	target, err := librarydb.Validate(incoming, next.ID, *next.Library)
	if err != nil {
		return nil, err
	}
	if old.ID == "" {
		old, err = a.store.RetainedLibrary(next.ID)
		var ambiguous *packages.AmbiguousRetainedLibraryError
		useTarget := targetFiles.generation != "" && (old.Version == next.Version || errors.As(err, &ambiguous))
		if err != nil && !useTarget {
			return nil, err
		}
	}
	if targetFiles.generation != "" && (old.ID == "" || old.Version == next.Version) {
		// A same-version reinstall keeps the existing work and audit.
		if targetFiles.registered != "" {
			if err := flushLibraryAudit(targetFiles); err != nil {
				return nil, err
			}
		}
		snapshot, err := librarydb.ValidateWorking(targetFiles.work, next.ID, *next.Library)
		if err != nil {
			return nil, err
		}
		if diffs := librarydb.StructureDifferences(snapshot, target); len(diffs) != 0 {
			return nil, fmt.Errorf("已保留的工作库与发布包不兼容（%s）", strings.Join(diffs, "；"))
		}
		if targetFiles.registered != "" {
			return nil, nil
		}
		targetFiles.fingerprint, err = libraryFingerprint(targetFiles)
		if err != nil {
			return nil, err
		}
		state, err := newCatalogLibraryState(next, targetFiles)
		if err != nil {
			return nil, err
		}
		state.before = &targetFiles
		return state, copyRetainedLibrary(targetFiles, state.directory, next)
	}
	state, err := newCatalogLibraryState(next, targetFiles)
	if err != nil {
		return nil, err
	}
	work := filepath.Join(state.directory, "work.library.db")
	if err := copyLibraryFile(incoming, filepath.Join(state.directory, "baseline.library.db")); err != nil {
		return state, err
	}
	entry := libraryAuditEntry{Time: time.Now().UTC(), Source: "upgrade", Action: "install", Version: next.Version}
	var oldAudit string
	if old.ID == "" {
		if err := copyLibraryFile(incoming, work); err != nil {
			return state, err
		}
	} else {
		baseline := filepath.Join(state.directory, "merge-base.library.db")
		defer os.Remove(baseline)
		contracts := []librarydb.Contract{*next.Library}
		if old.Library == nil {
			if err := stageLegacyJSONLibrary(old, next, incoming, work, baseline); err != nil {
				return state, err
			}
		} else {
			before, err := a.libraryFiles(old)
			if err != nil {
				return state, err
			}
			if before.registered != "" {
				if err := flushLibraryAudit(before); err != nil {
					return state, err
				}
			}
			before.fingerprint, err = libraryFingerprint(before)
			if err != nil {
				return state, err
			}
			state.before = &before
			if before.registered != "" && old.Version != next.Version {
				state.activation.PredecessorVersion = old.Version
				state.activation.PredecessorGeneration = before.registered
			}
			oldAudit = before.audit
			if err := copyLibraryFile(before.work, work); err != nil {
				return state, err
			}
			baseline, err = prepareLibraryMerge(work, before.baseline, old, next, target)
			if err != nil {
				return state, err
			}
			contracts = append(contracts, *old.Library)
		}
		plan, err := librarydb.Compare(work, baseline, incoming, target, *next.Library)
		if err != nil {
			return state, err
		}
		entry.Action = "upgrade"
		fmt.Fprintf(a.out, "业务库 %s %s → %s\n", next.Name, old.Version, next.Version)
		entry.Changes = a.reviewLibraryMerge(&plan, input, interactive, contracts...)
		if err := plan.Apply(work, target.DataVersion); err != nil {
			return state, err
		}
		if _, err := librarydb.ValidateWorking(work, next.ID, *next.Library); err != nil {
			return state, err
		}
	}
	return state, appendLibraryAudit(state.directory, oldAudit, entry)
}

func newCatalogLibraryState(p packages.Package, target libraryFiles) (*catalogLibraryState, error) {
	directory, err := newLibraryGeneration(target.root)
	if err != nil {
		return nil, err
	}
	return &catalogLibraryState{
		directory: directory,
		activation: packages.LibraryActivation{
			ID: p.ID, Version: p.Version, Previous: target.registered, Generation: filepath.Base(directory),
		},
	}, nil
}

func prepareLibraryMerge(work, base string, old, next packages.Package, target librarydb.Snapshot) (string, error) {
	local, err := librarydb.ValidateWorking(work, old.ID, *old.Library)
	if err != nil {
		return "", err
	}
	previous, err := librarydb.Validate(base, old.ID, *old.Library)
	if err != nil {
		return "", err
	}
	if differences := librarydb.StructureDifferences(local, previous); len(differences) > 0 {
		return "", fmt.Errorf("工作库与上一次发布包的结构不一致（%s）", strings.Join(differences, "；"))
	}
	if len(librarydb.StructureDifferences(local, target)) == 0 {
		return base, nil
	}
	migration, err := next.File(next.LibraryMigrations[strconv.Itoa(local.SchemaVersion)])
	if err != nil {
		return "", fmt.Errorf("缺少从结构版本 %d 到 %d 的迁移", local.SchemaVersion, target.SchemaVersion)
	}
	migratedBase := filepath.Join(filepath.Dir(work), "merge-base.library.db")
	if err := copyLibraryFile(base, migratedBase); err != nil {
		return "", err
	}
	for _, path := range []string{work, migratedBase} {
		if err := librarydb.Migrate(path, migration, local.SchemaVersion, target.SchemaVersion); err != nil {
			return "", err
		}
		migrated, err := librarydb.ValidateWorking(path, next.ID, *next.Library)
		if err != nil {
			return "", err
		}
		if differences := librarydb.StructureDifferences(migrated, target); len(differences) > 0 {
			return "", fmt.Errorf("迁移后与新版库结构不一致（%s）", strings.Join(differences, "；"))
		}
	}
	return migratedBase, nil
}

func (a *app) reviewLibraryMerge(plan *librarydb.Plan, input *bufio.Reader, interactive bool, contracts ...librarydb.Contract) []libraryAuditChange {
	var deletions []*librarydb.Change
	var audit []libraryAuditChange
	for i := range plan.Changes {
		change := &plan.Changes[i]
		key := libraryAuditKey(change.Table, change.Key, contracts...)
		record := fmt.Sprintf("表 %q，键 %q", change.Table, fmt.Sprint(key))
		switch {
		case change.Remote == nil:
			deletions = append(deletions, change)
			change.UseRemote = false
			note := ""
			if change.Conflict {
				note = "（本地记录已修改）"
			}
			fmt.Fprintf(a.out, "拟删除 %s%s\n", record, note)
		case change.Local == nil:
			action, selected := "add", "package"
			if change.Conflict {
				action, selected = "restore", "local"
				fmt.Fprintf(a.out, "记录冲突 %s：本地已删除，新版包已修改，保留本地删除。\n", record)
			} else {
				fmt.Fprintf(a.out, "新增 %s\n", record)
			}
			audit = append(audit, libraryAuditChange{
				Table: change.Table, Key: key, Action: action, Conflict: change.Conflict, Selected: selected,
			})
		default:
			for j := range change.Fields {
				field := &change.Fields[j]
				selected := "package"
				if field.Conflict {
					fmt.Fprintf(a.out, "字段冲突 %s，字段 %q\n", record, field.Name)
					field.UseRemote = false
					if interactive {
						local, remote := "[已脱敏]", "[已脱敏]"
						if !librarySensitive(change.Table, field.Name, contracts...) {
							local, remote = libraryPreviewValue(field.Local), libraryPreviewValue(field.Remote)
						}
						fmt.Fprintf(a.out, "  本地值 %s\n  新版包的值 %s\n", local, remote)
						fmt.Fprint(a.out, "保留本地值 [l]，或采用新版包的值 [r] [L/r] ")
						field.UseRemote = readChoice(input) == "r"
					}
					if !field.UseRemote {
						selected = "local"
						fmt.Fprintln(a.out, "保留本地值。")
					}
				} else {
					fmt.Fprintf(a.out, "修改 %s，字段 %q\n", record, field.Name)
				}
				audit = append(audit, libraryAuditChange{
					Table: change.Table, Key: key, Field: field.Name, Action: "update",
					Conflict: field.Conflict, Selected: selected,
				})
			}
		}
	}
	if len(deletions) > 0 {
		remove := false
		if interactive {
			fmt.Fprintf(a.out, "确认删除上述 %d 条记录 [y/N] ", len(deletions))
			choice := readChoice(input)
			remove = choice == "y" || choice == "yes"
		}
		for _, change := range deletions {
			key := libraryAuditKey(change.Table, change.Key, contracts...)
			change.UseRemote = remove
			selected := "package"
			if !remove {
				selected = "local"
				fmt.Fprintf(a.out, "保留拟删除记录：表 %q，键 %q\n", change.Table, fmt.Sprint(key))
			}
			audit = append(audit, libraryAuditChange{
				Table: change.Table, Key: key, Action: "delete", Conflict: change.Conflict, Selected: selected,
			})
		}
	}
	return audit
}

func librarySensitive(table, field string, contracts ...librarydb.Contract) bool {
	return slices.ContainsFunc(contracts, func(contract librarydb.Contract) bool { return contract.Sensitive(table, field) })
}

func libraryAuditKey(table string, key []any, contracts ...librarydb.Contract) []any {
	masked := slices.Clone(key)
	for _, contract := range contracts {
		for _, declared := range contract.Tables {
			if declared.Name == table {
				for i, field := range declared.Keys {
					if i < len(masked) && contract.Sensitive(table, field) {
						masked[i] = "[已脱敏]"
					}
				}
			}
		}
	}
	return masked
}

func libraryPreviewValue(value any) string {
	const limit = 120
	switch value := value.(type) {
	case string:
		for i := range value {
			if i >= limit {
				return libraryDisplayValue(value[:i]) + "…"
			}
		}
	case []byte:
		if len(value) > limit {
			return libraryDisplayValue(value[:limit]) + "…"
		}
	}
	return libraryDisplayValue(value)
}

func appendLibraryAudit(generation, previous string, entry libraryAuditEntry) error {
	path := filepath.Join(generation, "audit.jsonl")
	if previous != "" {
		if err := copyLibraryAudit(previous, path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	writeErr := json.NewEncoder(file).Encode(entry)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}
