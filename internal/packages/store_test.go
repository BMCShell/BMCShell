package packages

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bookID = "9ac22edd-c3ca-4324-95f6-5ca3cd709ff5"
const bundleID = "6e898dd4-72b1-4274-9b9d-93fac59dbeba"

func fixturePackage(t *testing.T, id, kind, name string, members ...string) map[string][]byte {
	t.Helper()
	m := Manifest{
		Schema: 1, CoreAPI: 1, ID: id, Type: kind, Name: name,
		Summary: "Test package", Version: "1.0.0", Help: "help.json", Changelog: "changelog.json",
		Members: members,
	}
	files := map[string][]byte{"help.json": []byte(`{"topics":{}}`), "changelog.json": []byte(`{"releases":[]}`)}
	if kind == "book" {
		m.Data = "book/fixture.book.db"
		files[m.Data] = []byte("SQLite format 3\x00")
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	files[ManifestFile] = manifest
	return files
}

func writeFixtureZIP(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	archive := zip.NewWriter(file)
	for name, data := range files {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestZIPInstallAndBundleGuard(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	book := writeFixtureZIP(t, fixturePackage(t, bookID, "book", "Fixture Book"))
	bundle := writeFixtureZIP(t, fixturePackage(t, bundleID, "bundle", "Fixture Bundle", bookID))
	if _, err := s.Install(bundle); err == nil {
		t.Fatal("accepted bundle without its member")
	}
	if _, err := s.Install(book); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Install(book); err == nil {
		t.Fatal("replaced an installed package")
	}
	p, err := s.Find("Fixture Book")
	if err != nil || p.ID != bookID {
		t.Fatalf("find by name = %q, %v", p.ID, err)
	}
	if _, err := s.Install(bundle); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(bookID); err == nil {
		t.Fatal("removed installed bundle member")
	}
	if err := s.Remove(bundleID); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(bookID); err != nil {
		t.Fatal(err)
	}
	if installed, err := s.List(); err != nil || len(installed) != 0 {
		t.Fatalf("list after removal = %v, %v", installed, err)
	}
}

func TestUnsafeZIPLeavesNoInstalledPackage(t *testing.T) {
	for _, path := range []string{"../escape", `book\escape`, "book/NUL .txt", "book/name:stream"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("GODEBUG", "zipinsecurepath=1") // Exercise the package store's own path checks.
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			files := fixturePackage(t, bookID, "book", "Fixture Book")
			files[path] = []byte("unsafe")
			if _, err := s.Install(writeFixtureZIP(t, files)); err == nil || !strings.Contains(err.Error(), "不符合路径要求") {
				t.Fatalf("unsafe path %q: %v", path, err)
			}
			if installed, err := s.List(); err != nil || len(installed) != 0 {
				t.Fatalf("list after rejected install = %v, %v", installed, err)
			}
		})
	}
}
