package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"bmcshell/internal/librarydb"
)

func TestLibraryOfflineHistoryRetention(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("BMCSHELL_HOME", profile)
	manifest := recoveryLibraryManifest()
	install := func(version string) libraryFiles {
		t.Helper()
		manifest.Version = version
		libraryCLI(t, 0, "", "plugin", "install", "--file", libraryFixture(t, manifest))
		_, state := installedLibrary(t, profile, manifest.ID)
		return state
	}
	first := install("1.0.0")
	a, _ := installedLibrary(t, profile, manifest.ID)
	p, err := a.store.Find(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	edit := librarydb.RecordEdit{
		Table: "records", Action: "update", Key: []any{"DEMO01"},
		Values: map[string]any{"value": "local changes"},
	}
	if err := a.mutateLibrary(p, edit, "manual", nil); err != nil {
		t.Fatal(err)
	}
	predecessor := install("2.0.0")
	current := install("1.5.0") // Time of activation, rather than version ordering, decides the predecessor.
	for _, name := range []string{"gen-duplicate", "gen-diverged", "gen-pending", "gen-unknown", "0.5.0/gen-legacy"} {
		if err := os.CopyFS(filepath.Join(current.root, name), os.DirFS(first.generation)); err != nil {
			t.Fatal(err)
		}
	}
	diverged := filepath.Join(current.root, "gen-diverged", "audit.jsonl")
	if err := os.WriteFile(diverged, []byte("{\"source\":\"manual\",\"action\":\"independent\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(current.root, "gen-pending", "work.library.db")
	libraryExec(t, pending, "CREATE TABLE _bmcshell_library_pending_audit (id INTEGER)")
	dbPath := filepath.Join(profile, "framework.db")
	libraryExec(t, dbPath, "INSERT INTO library_generations VALUES ('"+manifest.ID+"', '0.9.0', 'gen-unknown')")
	firstAudit, err := os.ReadFile(first.audit)
	if err != nil {
		t.Fatal(err)
	}
	activeAudit, err := os.ReadFile(current.audit)
	if err != nil || !bytes.HasPrefix(activeAudit, firstAudit) || len(firstAudit) == 0 {
		t.Fatal("the current audit must include the historical records")
	}

	backup := t.TempDir()
	if err := os.CopyFS(backup, os.DirFS(profile)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BMCSHELL_HOME", backup)
	restored, recovered := installedLibrary(t, backup, manifest.ID)
	checkValue := func(state libraryFiles) {
		t.Helper()
		value, err := librarydb.Query(state.work, manifest.ID, *manifest.Library, []string{"DEMO01"})
		if err != nil || value != "local changes" {
			t.Fatalf("restored working value = %q, %v", value, err)
		}
	}
	checkValue(recovered)
	restoredPath := func(name string) string {
		return filepath.Join(recovered.root, name)
	}
	duplicate := restoredPath("gen-duplicate")
	expected := []string{restoredPath(first.registered), duplicate}
	slices.Sort(expected)

	libraryCLI(t, 0, "", "plugin", "remove", manifest.Name)
	restoredDB := filepath.Join(backup, "framework.db")
	rollback := "{\"Previous\":[{\"ID\":\"" + manifest.ID + "\",\"Kind\":\"library\",\"Version\":\"1.0.0\"}]}"
	libraryExec(t, restoredDB, "INSERT INTO installed_package_rollback VALUES (1, '"+rollback+"')")
	protected, err := restored.store.PreviewLibraryRetention()
	if err != nil || !slices.Equal(protected, []string{duplicate}) {
		t.Fatalf("rollback snapshot became eligible for retirement: %v, %v", protected, err)
	}
	libraryExec(t, restoredDB, "DELETE FROM installed_package_rollback")
	preview, err := restored.store.PreviewLibraryRetention()
	if err != nil || !slices.Equal(preview, expected) {
		t.Fatalf("preview = %v, %v, want %v", preview, err, expected)
	}
	plan, err := restored.store.PlanLibraryRetention()
	if err != nil || !slices.Equal(plan, []string{duplicate}) {
		t.Fatalf("plan before retirement = %v, %v", plan, err)
	}
	if err := restored.store.RetireLibraryHistory([]string{duplicate}); err == nil {
		t.Fatal("retirement accepted a stale preview")
	}
	if err := restored.store.RetireLibraryHistory(preview); err != nil {
		t.Fatal(err)
	}
	plan, err = restored.store.PlanLibraryRetention()
	if err != nil || !slices.Equal(plan, expected) {
		t.Fatalf("plan after retirement = %v, %v, want %v", plan, err, expected)
	}
	if registered, err := a.store.LibraryGeneration(manifest.ID, "1.0.0"); err != nil || registered != first.registered {
		t.Fatalf("original profile unexpectedly changed: %q, %v", registered, err)
	}
	for version, generation := range map[string]string{"2.0.0": predecessor.registered, "1.5.0": current.registered, "0.9.0": "gen-unknown"} {
		if registered, err := restored.store.LibraryGeneration(manifest.ID, version); err != nil || registered != generation {
			t.Fatalf("protected registration %s = %q, %v", version, registered, err)
		}
	}
	for _, path := range plan {
		if err := os.Rename(path, filepath.Join(t.TempDir(), filepath.Base(path))); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := restored.store.RetainedLibrary(manifest.ID)
	if err != nil || latest.Version != "1.5.0" {
		t.Fatalf("retained version = %q, %v", latest.Version, err)
	}
	state, err := restored.libraryFiles(latest)
	if err != nil {
		t.Fatal(err)
	}
	checkValue(state)
	audit, err := os.ReadFile(state.audit)
	if err != nil || !bytes.Equal(audit, activeAudit) {
		t.Fatal("the current audit changed during retirement")
	}
}
