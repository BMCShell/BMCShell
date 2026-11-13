package settings

import (
	"reflect"
	"strings"
	"testing"
)

func TestMergeDraftPreservesUnrelatedEdits(t *testing.T) {
	base := Config{
		Theme:          "old",
		EnvDefaults:    map[string]string{"EXAMPLE": "old", "REMOVE": "old"},
		PluginSettings: map[string]map[string]string{"package": {"name": "old"}},
		ThemeSettings:  map[string]map[string]string{"theme": {"old": "old"}},
		Sources:        &[]Source{{Name: "existing", URL: "https://example.test/old"}},
	}
	edited := Config{
		Theme:          "new",
		EnvDefaults:    map[string]string{"EXAMPLE": ""},
		PluginSettings: map[string]map[string]string{"package": {"name": "new"}},
		Sources:        &[]Source{{Name: "existing", URL: "https://example.test/new"}},
	}
	current := Config{
		Theme:           "old",
		EnvDefaults:     map[string]string{"EXAMPLE": "old", "REMOVE": "old", "OTHER": "other"},
		DefaultResolver: &Resolver{ExtensionID: "existing", Export: "existing"},
		PluginSettings:  map[string]map[string]string{"package": {"name": "old", "other": "other"}},
		ThemeSettings:   map[string]map[string]string{"theme": {"old": "old", "other": "other"}},
		Sources: &[]Source{
			{Name: "existing", URL: "https://example.test/old"},
			{Name: "other", URL: "https://other.test"},
		},
		Proxy: "127.0.0.1:1080",
	}
	want := Config{
		Theme:           "new",
		EnvDefaults:     map[string]string{"EXAMPLE": "", "OTHER": "other"},
		DefaultResolver: current.DefaultResolver,
		PluginSettings:  map[string]map[string]string{"package": {"name": "new", "other": "other"}},
		ThemeSettings:   map[string]map[string]string{"theme": {"other": "other"}},
		Sources: &[]Source{
			{Name: "existing", URL: "https://example.test/new"},
			{Name: "other", URL: "https://other.test"},
		},
		Proxy: current.Proxy,
	}
	merged, err := MergeDraft(base, edited, current)
	if err != nil || !reflect.DeepEqual(merged, want) {
		t.Fatalf("merge = %+v, %v, want %+v", merged, err, want)
	}
	if current.EnvDefaults["EXAMPLE"] != "old" || current.PluginSettings["package"]["name"] != "old" ||
		current.ThemeSettings["theme"]["old"] != "old" || (*current.Sources)[0].URL != "https://example.test/old" {
		t.Fatal("merge mutated the saved snapshot")
	}
	replay, err := MergeDraft(base, edited, merged)
	if err != nil || !reflect.DeepEqual(replay, merged) {
		t.Fatalf("replay = %+v, %v, want %+v", replay, err, merged)
	}
}

func TestMergeDraftConflictIdentifiesSetting(t *testing.T) {
	base := Config{Sources: &[]Source{{Name: "other", URL: "https://other.test/old"}}}
	edited := Config{Sources: &[]Source{}}
	current := Config{Sources: &[]Source{{Name: "other", URL: "https://other.test/?token=sensitive"}}}
	_, err := MergeDraft(base, edited, current)
	if err == nil || !strings.Contains(err.Error(), "plugin.source.other") || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestMergeDraftConflictsWithConcurrentMigration(t *testing.T) {
	base := Config{ThemeSettings: map[string]map[string]string{"id": {"key": "old"}}}
	edited := Config{}
	current := Config{PluginSettings: map[string]map[string]string{"id": {"key": "new"}}}
	_, err := MergeDraft(base, edited, current)
	if err == nil || !strings.Contains(err.Error(), "plugin.id.key") {
		t.Fatalf("legacy reset must conflict with concurrent set: %v", err)
	}
	current.PluginSettings["id"]["key"] = "old"
	merged, err := MergeDraft(base, edited, current)
	if err != nil || len(merged.PluginSettings) != 0 || len(merged.ThemeSettings) != 0 {
		t.Fatalf("reset after a representation-only migration = %+v, %v", merged, err)
	}
}
