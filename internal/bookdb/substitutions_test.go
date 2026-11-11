package bookdb

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func substitutionBook(t *testing.T, options map[string]fieldOption, entries []Entry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "substitutions.book.db")
	if err := Create(path, entries); err != nil {
		t.Fatal(err)
	}
	db, err := open(path, "rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE book_meta SET field_options = ?", string(encoded)); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBookEnterpriseEnvironment(t *testing.T) {
	path := substitutionBook(t, map[string]fieldOption{"name": {Format: "oid"}}, []Entry{
		{ID: "1", Title: "enterprises.1.2.0", Body: "template"},
		{ID: "2", Title: "01.1.2.0", Body: "explicit numeric"},
		{ID: "3", Title: "enterprises_extra.1.2.0", Body: "plain text"},
	})
	for _, tc := range []struct{ environment, cid, expected string }{
		{"0", "", "0.1.2.0"},
		{"0", "C05", "0.1.2.0"},
		{"54357", "C05", "1.3.6.1.4.1.54357.1.2.0"},
		{"37945", "L0J", "1.3.6.1.4.1.37945.1.2.0"},
		{"37945", "C05", "1.3.6.1.4.1.37945.1.2.0"},
		{"1.3.6.1.4.1.12345", "L0J", "1.3.6.1.4.1.12345.1.2.0"},
		{"", "C05", "1.3.6.1.4.1.54357.1.2.0"},
		{"", "", "enterprises.1.2.0"},
	} {
		t.Run(tc.environment+"/"+tc.cid, func(t *testing.T) {
			t.Setenv(enterpriseVariable, tc.environment)
			for _, args := range [][]string{{"--id", "1"}, {"--name", "enterprises.1.2.0"}, {"--name", tc.expected}} {
				var out bytes.Buffer
				if err := Query(&out, path, args, tc.cid); err != nil || !strings.Contains(out.String(), tc.expected) {
					t.Fatalf("query %v: %v\n%s", args, err, &out)
				}
			}
		})
	}
	t.Setenv(enterpriseVariable, "0")
	for _, entry := range []Entry{{ID: "2", Title: "01.1.2.0"}, {ID: "3", Title: "enterprises_extra.1.2.0"}} {
		var out bytes.Buffer
		if err := Query(&out, path, []string{"--name", entry.Title}, "C05"); err != nil || !strings.Contains(out.String(), entry.Title) {
			t.Fatalf("preserve prefix boundary: %v\n%s", err, &out)
		}
	}
	t.Setenv(enterpriseVariable, "bad\nvalue")
	if err := Query(&bytes.Buffer{}, path, nil, ""); err == nil || strings.Contains(err.Error(), "bad") {
		t.Fatalf("expected a value-free validation error, got %v", err)
	}
}

func TestBookCanonicalEnterpriseEnvironment(t *testing.T) {
	const template = "1.3.6.1.4.1.enterprises.1.2.0"
	path := substitutionBook(t, map[string]fieldOption{"name": {Format: "oid"}}, []Entry{
		{ID: "1", Title: template, Body: "canonical template"},
		{ID: "2", Title: "1.3.6.1.4.1.enterprises_extra.1.2.0", Body: "plain text"},
		{ID: "3", Title: "1.3.6.1.4.1.54357.1.2.0", Body: "literal numeric"},
		{ID: "4", Title: "1.3.6.1.4.1.enterprises", Body: "canonical root"},
		{ID: "5", Title: ".1.3.6.1.4.1.enterprises.1.2.0", Body: "leading dot template"},
	})
	for _, tc := range []struct{ environment, cid, expected string }{
		{"0", "", "1.3.6.1.4.1.0.1.2.0"},
		{"0", "C05", "1.3.6.1.4.1.0.1.2.0"},
		{"54357", "C05", "1.3.6.1.4.1.54357.1.2.0"},
		{"37945", "L0J", "1.3.6.1.4.1.37945.1.2.0"},
		{"1.3.6.1.4.1.12345", "C05", "1.3.6.1.4.1.12345.1.2.0"},
		{"1.3.6.1", "C05", "1.3.6.1.1.2.0"},
		{"", "C05", "1.3.6.1.4.1.54357.1.2.0"},
		{"", "L0J", "1.3.6.1.4.1.37945.1.2.0"},
		{"", "", template},
	} {
		t.Run(tc.environment+"/"+tc.cid, func(t *testing.T) {
			t.Setenv(enterpriseVariable, tc.environment)
			for _, args := range [][]string{
				{"--id", "1"}, {"--name", template}, {"--name", "enterprises.1.2.0"}, {tc.expected},
			} {
				var out bytes.Buffer
				if err := Query(&out, path, args, tc.cid); err != nil ||
					!strings.Contains(out.String(), "canonical template") ||
					!strings.Contains(out.String(), tc.expected) {
					t.Fatalf("query %v: %v\n%s", args, err, &out)
				}
			}
			root := strings.TrimSuffix(tc.expected, ".1.2.0")
			var out bytes.Buffer
			if err := Query(&out, path, []string{"--name", root}, tc.cid); err != nil ||
				!strings.Contains(out.String(), "canonical root") || !strings.Contains(out.String(), root) {
				t.Fatalf("root query: %v\n%s", err, &out)
			}
			out.Reset()
			if err := Query(&out, path, []string{"--name", "." + tc.expected}, tc.cid); err != nil ||
				!strings.Contains(out.String(), "leading dot template") || !strings.Contains(out.String(), "."+tc.expected) {
				t.Fatalf("leading dot query: %v\n%s", err, &out)
			}
		})
	}
	t.Setenv(enterpriseVariable, "0")
	for _, title := range []string{"1.3.6.1.4.1.enterprises_extra.1.2.0", "1.3.6.1.4.1.54357.1.2.0"} {
		var out bytes.Buffer
		if err := Query(&out, path, []string{"--name", title}, ""); err != nil ||
			!strings.Contains(out.String(), title) {
			t.Fatalf("literal OID query %q: %v\n%s", title, err, &out)
		}
	}
}

func TestBookDeclaredSubstitutions(t *testing.T) {
	t.Setenv("BMCSHELL_TARGET_MODEL", "west")
	t.Setenv("BMCSHELL_TARGET_RESOLVER", "{{region}}")
	path := substitutionBook(t, map[string]fieldOption{
		"name": {Substitutions: map[string]string{"{{region}}": "BMCSHELL_TARGET_MODEL", "{{other}}": "BMCSHELL_TARGET_RESOLVER"}},
	}, []Entry{{ID: "1", Title: "{{region}} / {{other}} / {{region}}", Body: "{{region}} in raw body"}})
	var out bytes.Buffer
	if err := Query(&out, path, []string{"--id", "1"}, ""); err != nil ||
		!strings.Contains(out.String(), "west / {{region}} / west") ||
		!strings.Contains(out.String(), "{{region}} in raw body") {
		t.Fatalf("single-pass declared field: %v\n%s", err, &out)
	}
	path = substitutionBook(t, map[string]fieldOption{
		"content": {Substitutions: map[string]string{"{{region}}": "BMCSHELL_TARGET_MODEL"}},
	}, []Entry{{ID: "1", Title: "{{region}}", Body: "{{region}} in body"}})
	out.Reset()
	if err := Query(&out, path, []string{"--id", "1"}, ""); err != nil ||
		!strings.Contains(out.String(), "{{region}}") || !strings.Contains(out.String(), "west in body") {
		t.Fatalf("declared body: %v\n%s", err, &out)
	}
}

func TestBookSubstitutionValidationAndSize(t *testing.T) {
	for _, options := range []map[string]fieldOption{
		{"name": {Substitutions: map[string]string{"{{token}}": "BMCSHELL_AUTH_PASSWORD"}}},
		{"name": {Substitutions: map[string]string{"token": "BMCSHELL_TARGET_MODEL"}}},
		{"name": {Substitutions: map[string]string{"{{token}}": "ACME_UNREGISTERED_FIELD"}}},
		{"id": {Format: "integer", Substitutions: map[string]string{"{{token}}": "BMCSHELL_TARGET_MODEL"}}},
	} {
		path := substitutionBook(t, options, []Entry{{ID: "1", Title: "title"}})
		if err := Query(&bytes.Buffer{}, path, nil, ""); err == nil {
			t.Fatal("invalid substitutions were accepted")
		}
	}
	t.Setenv("BMCSHELL_TARGET_MODEL", strings.Repeat("x", 256))
	path := substitutionBook(t, map[string]fieldOption{
		"content": {Substitutions: map[string]string{"{{token}}": "BMCSHELL_TARGET_MODEL"}},
	}, []Entry{{ID: "1", Title: "title", Body: strings.Repeat("{{token}}", bodyLimit/256+1)}})
	if err := Query(&bytes.Buffer{}, path, []string{"--id", "1"}, ""); err == nil {
		t.Fatal("oversized rendered body was accepted")
	}
}

func TestBookOIDLeadingDot(t *testing.T) {
	if got := swapOIDPrefix(".enterprises.1.2.0", "enterprises", "0"); got != ".0.1.2.0" {
		t.Fatalf("leading dot substitution = %q", got)
	}
	if got := swapOIDPrefix("01.2.0", "0", "enterprises"); got != "01.2.0" {
		t.Fatalf("partial prefix changed = %q", got)
	}
}
