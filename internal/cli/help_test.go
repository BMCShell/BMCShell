package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/internal/bookdb"
	"bmcshell/internal/packages"
)

func TestBuiltinHelpEntryPoints(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	t.Setenv("BMCSHELL_HOME", home)
	for _, key := range sortedKeys(builtinHelp) {
		words := strings.Fields(key)
		explicit := libraryCLI(t, 0, "", append(words, "--help")...)
		lookup := libraryCLI(t, 0, "", append([]string{"help"}, words...)...)
		if explicit != lookup || !strings.Contains(explicit, "\n用法\n") || !strings.Contains(explicit, "\n示例\n") {
			t.Errorf("help for %q differs or is incomplete: %q / %q", key, explicit, lookup)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("built-in help used the data directory: %v", err)
	}
}

func TestDataPackageHelpEntryPoints(t *testing.T) {
	t.Setenv("BMCSHELL_HOME", t.TempDir())

	book := catalogManifest("4f39cb16-7e66-4fe2-9afe-fdfa56e7f005", "catalog-book-demo", "book", "1.0.0")
	book.ShortName, book.Data = "demo", "fixture.book.db"
	bookDir := catalogFixture(t, book)
	bookData := filepath.Join(bookDir, book.Data)
	if err := os.Remove(bookData); err != nil {
		t.Fatal(err)
	}
	if err := bookdb.Create(bookData, []bookdb.Entry{{ID: "1", Title: "示例记录"}}); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", bookDir)

	library := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "807d99b2-7d71-4c47-9908-3c40826410f0", Type: "library",
		Name: "示例库", Summary: "旧版 JSON 记录",
		Version: "1.0.0", Help: "help.json", Changelog: "changelog.json", Data: "records.json",
	}
	libraryDir := libraryFixture(t, library)
	if err := os.WriteFile(filepath.Join(libraryDir, library.Help), []byte(`{"topics":{"package":{"summary":"查看示例库帮助。","usage":["bmc library 示例库"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, 0, "", "plugin", "install", "--file", libraryDir)

	t.Setenv("BMCSHELL_CONTEXT", "invalid context")
	for _, topic := range []struct{ kind, name string }{{"book", book.ShortName}, {"library", library.Name}} {
		explicit := libraryCLI(t, 0, "", topic.kind, topic.name, "--help")
		lookup := libraryCLI(t, 0, "", "help", topic.kind, topic.name)
		if explicit != lookup || !strings.Contains(explicit, "bmc "+topic.kind+" "+topic.name) {
			t.Errorf("%s %s help differs or omits the command: %q / %q", topic.kind, topic.name, explicit, lookup)
		}
	}
}
