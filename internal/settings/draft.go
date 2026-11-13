package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"bmcshell/internal/filelock"
)

const draftFile = "settings.draft.json"

// Draft is an exclusive editing session. Base is the saved configuration at
// the start of the session, and Config is the independently editable copy.
type Draft struct {
	Base      Config
	Config    Config
	Recovered bool

	root    string
	release func()
}

type draftCheckpoint struct {
	Base   *Config `json:"base"`
	Config *Config `json:"config"`
}

// OpenDraft loads any checkpoint and locks it until Close. Commands writing
// the saved configuration use a separate lock and can run during editing.
func OpenDraft(root string) (*Draft, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	release, err := filelock.Acquire(filepath.Join(root, ".settings-draft.lock"))
	if err != nil {
		return nil, fmt.Errorf("无法打开设置草稿：%w", err)
	}
	d := &Draft{root: root, release: release}
	if err := d.load(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *Draft) load() error {
	data, err := os.ReadFile(filepath.Join(d.root, draftFile))
	if err == nil {
		var saved draftCheckpoint
		if err := json.Unmarshal(data, &saved); err != nil || saved.Base == nil || saved.Config == nil {
			return errors.New("设置草稿文件无效")
		}
		d.Base, d.Config, d.Recovered = *saved.Base, *saved.Config, true
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d.Base, err = Load(d.root)
	if err != nil {
		return err
	}
	d.Config = Clone(d.Base)
	return nil
}

// Dirty compares the saved forms so an empty optional map and an absent map
// represent the same configuration.
func (d *Draft) Dirty() bool {
	before, beforeErr := json.Marshal(d.Base)
	after, afterErr := json.Marshal(d.Config)
	return beforeErr != nil || afterErr != nil || !bytes.Equal(before, after)
}

// Checkpoint replaces the draft atomically after a menu edit. Reverting every
// change removes the previous checkpoint.
func (d *Draft) Checkpoint() error {
	if d.release == nil {
		return errors.New("设置草稿已关闭")
	}
	if !d.Dirty() {
		return d.Discard()
	}
	data, err := json.MarshalIndent(draftCheckpoint{Base: &d.Base, Config: &d.Config}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(d.root, ".settings-draft-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(d.root, draftFile))
}

// Discard removes the checkpoint. Close the session afterwards to release the
// edit lock.
func (d *Draft) Discard() error {
	if d.release == nil {
		return errors.New("设置草稿已关闭")
	}
	err := os.Remove(filepath.Join(d.root, draftFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Close releases the editing lock and leaves any checkpoint available.
func (d *Draft) Close() {
	if d.release != nil {
		d.release()
		d.release = nil
	}
}
