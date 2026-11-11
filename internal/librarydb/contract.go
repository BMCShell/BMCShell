// Package librarydb validates and queries BMCShell SQLite Library snapshots.
package librarydb

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const metadataTable = "_bmcshell_library"
const pendingAuditTable = "_bmcshell_library_pending_audit"

var libraryID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Contract struct {
	SchemaVersion int     `json:"schemaVersion"`
	Tables        []Table `json:"tables"`
	Lookup        Lookup  `json:"lookup"`
}

type Table struct {
	Name            string   `json:"name"`
	Keys            []string `json:"keys"`
	SensitiveFields []string `json:"sensitiveFields,omitempty"`
}

type Lookup struct {
	Table string `json:"table"`
	Value string `json:"value"`
}

func (c Contract) Validate() error {
	if c.SchemaVersion < 1 || c.SchemaVersion > math.MaxInt32 || len(c.Tables) == 0 {
		return fmt.Errorf("Library needs a positive SQLite schemaVersion and at least one table")
	}
	seen := make(map[string]bool)
	for _, table := range c.Tables {
		name := strings.ToLower(table.Name)
		if !validIdentifier(table.Name) || (name == metadataTable || name == pendingAuditTable) || strings.HasPrefix(name, "sqlite_") || seen[name] {
			return fmt.Errorf("invalid or duplicate Library table %q", table.Name)
		}
		seen[name] = true
		if len(table.Keys) == 0 {
			return fmt.Errorf("Library table %q needs a stable key", table.Name)
		}
		keys := make(map[string]bool)
		for _, key := range table.Keys {
			folded := strings.ToLower(key)
			if !validIdentifier(key) || keys[folded] {
				return fmt.Errorf("invalid or duplicate key %q for Library table %q", key, table.Name)
			}
			keys[folded] = true
		}
		sensitive := make(map[string]bool)
		for _, field := range table.SensitiveFields {
			folded := strings.ToLower(field)
			if !validIdentifier(field) || sensitive[folded] {
				return fmt.Errorf("invalid or duplicate sensitive field %q for Library table %q", field, table.Name)
			}
			sensitive[folded] = true
		}
	}
	if c.Lookup == (Lookup{}) {
		return nil
	}
	if !validIdentifier(c.Lookup.Value) || c.lookupTable().Name == "" {
		return fmt.Errorf("Library lookup needs a declared table and a value column")
	}
	return nil
}

// StructurallyEqual compares declarations that affect the schema and its lookup.
func (c Contract) StructurallyEqual(other Contract) bool {
	return c.SchemaVersion == other.SchemaVersion && c.Lookup == other.Lookup &&
		slices.EqualFunc(c.Tables, other.Tables, func(left, right Table) bool {
			return left.Name == right.Name && slices.Equal(left.Keys, right.Keys)
		})
}

// Sensitive reports whether an operator should be shown a redacted field.
func (c Contract) Sensitive(table, field string) bool {
	folded := strings.ToLower(field)
	for _, declared := range c.Tables {
		if declared.Name == table && slices.ContainsFunc(declared.SensitiveFields, func(name string) bool {
			return strings.EqualFold(name, field)
		}) {
			return true
		}
	}
	folded = strings.NewReplacer("-", "", "_", "", " ", "").Replace(folded)
	for _, secret := range []string{"password", "passwd", "passphrase", "credential", "secret", "token", "apikey", "privatekey", "community", "cookie"} {
		if strings.Contains(folded, secret) {
			return true
		}
	}
	return folded == "pwd"
}

func (c Contract) lookupTable() Table {
	for _, table := range c.Tables {
		if table.Name == c.Lookup.Table {
			return table
		}
	}
	return Table{}
}

func validIdentifier(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl)
}

// QuoteIdentifier quotes a SQLite table or column name. Callers must still bind data values as parameters.
func QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
