package settings

import (
	"errors"
	"testing"

	"bmcshell/internal/filelock"
)

func TestDraftCheckpointRecovery(t *testing.T) {
	root := t.TempDir()
	original := Config{
		Theme:       "old",
		EnvDefaults: map[string]string{"EXAMPLE": "old"},
	}
	if err := Save(root, original); err != nil {
		t.Fatal(err)
	}
	draft, err := OpenDraft(root)
	if err != nil {
		t.Fatal(err)
	}
	defer draft.Close()
	if draft.Dirty() || draft.Recovered {
		t.Fatal("fresh draft should be clean")
	}
	if other, err := OpenDraft(root); !errors.Is(err, filelock.ErrBusy) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("second editor error = %v", err)
	}
	draft.Config.Theme = "new"
	draft.Config.EnvDefaults["EXAMPLE"] = ""
	if !draft.Dirty() || draft.Base.EnvDefaults["EXAMPLE"] != "old" {
		t.Fatal("draft editing mutated the baseline")
	}
	if err := draft.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if err := Update(root, func(cfg *Config) error {
		cfg.EnvDefaults["OTHER"] = "concurrent"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	draft.Close()

	draft, err = OpenDraft(root)
	if err != nil {
		t.Fatal(err)
	}
	defer draft.Close()
	if !draft.Recovered || draft.Base.Theme != "old" || draft.Config.Theme != "new" {
		t.Fatalf("unexpected recovery: %+v", draft)
	}
	if value, exists := draft.Config.EnvDefaults["EXAMPLE"]; !exists || value != "" {
		t.Fatalf("explicit empty value did not survive recovery: %q, %v", value, exists)
	}
	if err := draft.Discard(); err != nil {
		t.Fatal(err)
	}
	draft.Close()

	draft, err = OpenDraft(root)
	if err != nil {
		t.Fatal(err)
	}
	defer draft.Close()
	if draft.Recovered || draft.Dirty() || draft.Config.Theme != "old" ||
		draft.Config.EnvDefaults["OTHER"] != "concurrent" || draft.Config.EnvDefaults["EXAMPLE"] != "old" {
		t.Fatalf("discard did not retain saved configuration: %+v", draft)
	}
}

func TestDraftRevertRemovesCheckpoint(t *testing.T) {
	root := t.TempDir()
	draft, err := OpenDraft(root)
	if err != nil {
		t.Fatal(err)
	}
	defer draft.Close()
	draft.Config.EnvDefaults = map[string]string{"EXAMPLE": "new"}
	if err := draft.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	delete(draft.Config.EnvDefaults, "EXAMPLE")
	if draft.Dirty() {
		t.Fatal("the same saved representation should be clean")
	}
	if err := draft.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	draft.Close()
	draft, err = OpenDraft(root)
	if err != nil {
		t.Fatal(err)
	}
	defer draft.Close()
	if draft.Recovered {
		t.Fatal("fully reverted draft was recovered")
	}
}
