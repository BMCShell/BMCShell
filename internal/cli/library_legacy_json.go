package cli

import (
	"errors"
	"path/filepath"
	"slices"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

// Legacy JSON was a read-only key/value payload. Import it as local data so an
// older record is retained and a differing new value requires a conflict choice.
func stageLegacyJSONLibrary(old, next packages.Package, incoming, work, baseline string) error {
	tables := next.Library.Tables
	if len(tables) != 1 || tables[0].Name != "records" || !slices.Equal(tables[0].Keys, []string{"key"}) ||
		next.Library.Lookup != (librarydb.Lookup{Table: "records", Value: "value"}) {
		return errors.New("旧版 JSON Library 只能迁移到 records 表的 key/value 查询库")
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
		if err := librarydb.ImportLegacyKeyValues(path, records); err != nil {
			return errors.New("迁移 " + filepath.Base(path) + "：" + err.Error())
		}
	}
	return nil
}
