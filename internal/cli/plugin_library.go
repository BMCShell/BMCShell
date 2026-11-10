package cli

import (
	"errors"
	"os"
	"path/filepath"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

type catalogLibraryState struct {
	before     libraryFiles
	root       string
	previous   string
	generation string
	activated  bool
}

// commitCatalogPlan keeps synchronized data when an ordinary Library package
// upgrade preserves its database contract. Schema migrations use library refresh.
func (a *app) commitCatalogPlan(plan *packages.Upgrade) (err error) {
	installed := make(map[string]packages.Package)
	for _, p := range plan.OriginalPackages() {
		installed[p.ID] = p
	}
	var states []*catalogLibraryState
	var locks []func()
	defer func() {
		if err != nil {
			for _, state := range states {
				err = errors.Join(err, state.restore())
			}
		}
		for _, unlock := range locks {
			unlock()
		}
	}()
	for _, next := range plan.Packages() {
		old, exists := installed[next.ID]
		if !exists || old.Version == next.Version || next.Library == nil {
			continue
		}
		unlock, err := a.lockLibrary(next.ID)
		if err != nil {
			return err
		}
		locks = append(locks, unlock)
		state, err := a.stageCatalogLibrary(old, next)
		if state != nil {
			states = append(states, state)
		}
		if err != nil {
			return err
		}
	}
	return a.store.CommitChanges(plan, func() error {
		for _, state := range states {
			if err := checkLibraryGeneration(state.before); err != nil {
				return err
			}
			if err := state.activate(); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *app) stageCatalogLibrary(old, next packages.Package) (*catalogLibraryState, error) {
	before, err := a.libraryFiles(old)
	if err != nil {
		return nil, err
	}
	before.fingerprint, err = libraryFingerprint(before)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(a.home, "libraries", next.ID, next.Version)
	previous, err := readLibraryGeneration(root)
	if err != nil {
		return nil, err
	}
	state := &catalogLibraryState{before: before, root: root, previous: previous}
	if before.active == "" {
		return state, nil
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return state, err
	}
	state.generation, err = os.MkdirTemp(root, "gen-")
	if err != nil {
		return state, err
	}
	for _, file := range []struct{ source, name string }{
		{before.work, "work.library.db"},
		{before.baseline, "baseline.library.db"},
	} {
		destination := filepath.Join(state.generation, file.name)
		if err := copyLibraryFile(file.source, destination); err != nil {
			return state, err
		}
		if _, err := librarydb.Validate(destination, next.ID, *next.Library); err != nil {
			return state, err
		}
	}
	return state, nil
}

func (s *catalogLibraryState) activate() error {
	current, err := readLibraryGeneration(s.root)
	if err != nil {
		return err
	}
	if current != s.previous {
		return errors.New("target Library generation changed before package activation")
	}
	if s.generation != "" {
		if err := activateLibraryGeneration(s.root, s.generation); err != nil {
			return err
		}
		s.activated = true
	} else if s.previous != "" {
		if err := os.Remove(filepath.Join(s.root, "active")); err != nil {
			return err
		}
		s.activated = true
	}
	return nil
}

func (s *catalogLibraryState) restore() error {
	if s.activated {
		if s.previous != "" {
			if err := activateLibraryGeneration(s.root, s.previous); err != nil {
				return err
			}
		} else if err := os.Remove(filepath.Join(s.root, "active")); err != nil {
			return err
		}
	}
	if s.generation != "" {
		return os.RemoveAll(s.generation)
	}
	return nil
}
