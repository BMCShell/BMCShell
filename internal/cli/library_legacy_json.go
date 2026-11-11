package cli

import (
	"fmt"
	"path/filepath"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

// Legacy JSON was a read-only key/value payload. Import it as local data so an
// older record is retained and a differing new value requires a conflict choice.
func stageLegacyJSONLibrary(old, next packages.Package, incoming, work, baseline string) error {
	tables := next.Library.Tables
	lookup := next.Library.Lookup
	if len(tables) != 1 || len(tables[0].Keys) != 1 || lookup.Table != tables[0].Name ||
		lookup.Value == "" || lookup.Value == tables[0].Keys[0] {
		return fmt.Errorf("迁移旧版 JSON 业务库需要一张业务表、一个稳定键和单独的 lookup 值列")
	}
	var entries []librarydb.LegacyKeyValue
	if err := readPackageJSON(old, old.Data, &entries); err != nil {
		return err
	}
	for _, path := range []string{work, baseline} {
		if err := copyLibraryFile(incoming, path); err != nil {
			return err
		}
		records := entries
		if path == baseline {
			records = nil
		}
		if err := librarydb.ImportLegacyKeyValues(path, tables[0], lookup.Value, records); err != nil {
			return fmt.Errorf("迁移 %s：%w", filepath.Base(path), err)
		}
	}
	return nil
}
