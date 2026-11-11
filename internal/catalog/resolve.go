package catalog

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"bmcshell/internal/librarydb"
	"bmcshell/internal/packages"
)

// Match finds the newest matching build of each distinct package ID.
func Match(entries []Entry, selector, source string) ([]Entry, error) {
	ids := make(map[string]bool)
	for _, entry := range entries {
		if source != "" && entry.Source != source {
			continue
		}
		if strings.EqualFold(entry.ID, selector) {
			ids = map[string]bool{entry.ID: true}
			break
		}
		if entry.Name == selector {
			ids[entry.ID] = true
		}
	}
	var matches []Entry
	for _, id := range orderedKeys(ids) {
		versions, err := versionsFor(entries, id, source)
		if err != nil {
			return nil, err
		}
		matches = append(matches, versions[0])
	}
	return matches, nil
}

func versionsFor(entries []Entry, id, source string) ([]Entry, error) {
	var versions []Entry
	for _, entry := range entries {
		if entry.ID == id && (source == "" || entry.Source == source) {
			versions = append(versions, entry)
		}
	}
	if source == "" && len(versions) != 0 {
		for _, entry := range versions[1:] {
			if entry.Source != versions[0].Source {
				return nil, fmt.Errorf("package %s is available from %s and %s; review configured sources in bmc setting", id, versions[0].Source, entry.Source)
			}
		}
	}
	sort.SliceStable(versions, func(i, j int) bool {
		return CompareVersion(versions[i].Version, versions[j].Version) > 0
	})
	return versions, nil
}

func ResolveInstall(entries []Entry, installed []packages.Package, selectors []string, source string) ([]Entry, error) {
	s := newSolver(entries, installed, source, false)
	for _, selector := range selectors {
		matches, err := Match(entries, selector, source)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("package %q was not found in the cached indexes; run bmc plugin update", selector)
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("ambiguous package name %q; select a UUID", selector)
		}
		id := matches[0].ID
		if _, ok := s.current[id]; ok {
			return nil, fmt.Errorf("package %s is already installed; use bmc plugin upgrade", selector)
		}
		s.targets[id] = true
	}
	return s.solve()
}

// UpgradeCandidates lists new compatible-schema versions before dependency resolution.
func UpgradeCandidates(entries []Entry, installed []packages.Package, source string) ([]Entry, error) {
	var result []Entry
	for _, current := range installed {
		versions, err := upgradeVersions(entries, current, source, 0)
		if err != nil {
			return nil, err
		}
		if len(versions) > 0 {
			result = append(result, versions[0])
		}
	}
	return result, nil
}

func ResolveUpgrade(entries []Entry, installed []packages.Package, selectors []string, source string) ([]Entry, error) {
	s := newSolver(entries, installed, source, true)
	if len(selectors) == 0 {
		candidates, err := UpgradeCandidates(entries, installed, source)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			s.targets[candidate.ID] = true
		}
	}
	for _, selector := range selectors {
		var ids []string
		if current, ok := s.current[strings.ToLower(selector)]; ok {
			ids = append(ids, current.ID)
		} else {
			for _, current := range installed {
				if current.Name == selector {
					ids = append(ids, current.ID)
				}
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("package %q is not installed", selector)
		}
		if len(ids) != 1 {
			return nil, fmt.Errorf("ambiguous installed package %q; select a UUID", selector)
		}
		versions, err := upgradeVersions(entries, s.current[ids[0]], source, 0)
		if err != nil {
			return nil, err
		}
		if len(versions) == 0 {
			return nil, fmt.Errorf("no newer compatible version for %q", selector)
		}
		s.targets[ids[0]] = true
	}
	if len(s.targets) == 0 {
		return nil, nil
	}
	return s.solve()
}

func ResolveLibrary(entries []Entry, installed []packages.Package, libraryID string, schemaVersion int, source string) ([]Entry, error) {
	s := newSolver(entries, installed, source, true)
	library, ok := s.current[libraryID]
	if !ok || library.Library == nil || schemaVersion <= library.Library.SchemaVersion {
		return nil, errors.New("Library source resolution requires an installed SQLite Library and a newer schema")
	}
	s.libraryID, s.librarySchema = libraryID, schemaVersion
	s.targets[libraryID] = true
	s.affected[libraryID] = true
	for changed := true; changed; {
		changed = false
		for _, p := range installed {
			if s.affected[p.ID] {
				continue
			}
			for id := range s.affected {
				if directlyDepends(p.Manifest, id) {
					s.affected[p.ID], changed = true, true
					break
				}
			}
		}
	}
	return s.solve()
}

type selection struct {
	manifest packages.Manifest
	entry    *Entry
}

type solver struct {
	entries       []Entry
	current       map[string]packages.Package
	targets       map[string]bool
	affected      map[string]bool
	domains       map[string][]selection
	domainErrors  map[string]error
	source        string
	upgrade       bool
	libraryID     string
	librarySchema int
	steps         int
	conflict      error
}

func newSolver(entries []Entry, installed []packages.Package, source string, upgrade bool) *solver {
	s := &solver{
		entries: entries, source: source, upgrade: upgrade,
		current: make(map[string]packages.Package), targets: make(map[string]bool), affected: make(map[string]bool),
		domains: make(map[string][]selection), domainErrors: make(map[string]error),
	}
	for _, p := range installed {
		s.current[p.ID] = p
	}
	return s
}

func upgradeVersions(entries []Entry, current packages.Package, source string, requiredSchema int) ([]Entry, error) {
	if source == "" {
		source = current.Source
	}
	oldSchema := schemaOf(current.Manifest)
	var eligible []Entry
	for _, entry := range entries {
		if entry.ID != current.ID || packages.CanonicalType(entry.Type) != packages.CanonicalType(current.Type) || CompareVersion(entry.Version, current.Version) <= 0 {
			continue
		}
		if requiredSchema == 0 && entry.LibrarySchemaVersion != oldSchema &&
			(entry.LibrarySchemaVersion <= oldSchema || entry.LibraryMigrations[strconv.Itoa(oldSchema)] == "") {
			continue
		}
		if requiredSchema != 0 && (entry.LibrarySchemaVersion != requiredSchema ||
			entry.LibraryMigrations[strconv.Itoa(oldSchema)] == "") {
			continue
		}
		eligible = append(eligible, entry)
	}
	return versionsFor(eligible, current.ID, source)
}

func (s *solver) domain(id string) []selection {
	if cached, ok := s.domains[id]; ok {
		return cached
	}
	current, installed := s.current[id]
	var choices []selection
	if installed && !s.targets[id] && s.libraryAllows(current.Manifest, current.Manifest) {
		choices = append(choices, selection{manifest: current.Manifest})
	}
	var versions []Entry
	var err error
	source := ""
	if s.targets[id] {
		source = s.source
	}
	switch {
	case !installed && s.libraryID == "":
		versions, err = versionsFor(s.entries, id, source)
		if err != nil && source == "" && s.source != "" {
			if preferred, _ := versionsFor(s.entries, id, s.source); len(preferred) > 0 {
				versions, err = preferred, nil
			}
		}
	case installed && s.upgrade && (s.libraryID == "" || id == s.libraryID || s.affected[id] && current.Type != "library"):
		requiredSchema := 0
		if id == s.libraryID {
			requiredSchema = s.librarySchema
		}
		versions, err = upgradeVersions(s.entries, current, source, requiredSchema)
		if err != nil && source == "" && current.Source == "" && s.source != "" {
			if preferred, _ := upgradeVersions(s.entries, current, s.source, requiredSchema); len(preferred) > 0 {
				versions, err = preferred, nil
			}
		}
	}
	if err != nil {
		s.domainErrors[id] = err
	}
	for _, entry := range versions {
		m := entryManifest(entry)
		if s.libraryAllows(current.Manifest, m) {
			choices = append(choices, selection{manifest: m, entry: &entry})
		}
	}
	s.domains[id] = choices
	return choices
}

func (s *solver) libraryAllows(old, next packages.Manifest) bool {
	return s.libraryID == "" || !directlyDepends(old, s.libraryID) && !directlyDepends(next, s.libraryID) ||
		next.LibrarySchemas[s.libraryID].Supports(s.librarySchema)
}

func directlyDepends(m packages.Manifest, id string) bool {
	_, dependency := m.Dependencies[id]
	_, schema := m.LibrarySchemas[id]
	return dependency || schema
}

func entryManifest(entry Entry) packages.Manifest {
	m := packages.Manifest{
		ID: entry.ID, Name: entry.Name, Type: entry.Type, Version: entry.Version,
		Dependencies: entry.Dependencies, Members: entry.Members, LibrarySchemas: entry.LibrarySchemas,
	}
	if entry.LibrarySchemaVersion != 0 {
		m.Library = &librarydb.Contract{SchemaVersion: entry.LibrarySchemaVersion}
	}
	return m
}

func schemaOf(m packages.Manifest) int {
	if m.Library != nil {
		return m.Library.SchemaVersion
	}
	return 0
}

func (s *solver) solve() ([]Entry, error) {
	required := make(map[string]bool, len(s.current)+len(s.targets))
	for id := range s.current {
		required[id] = true
	}
	for id := range s.targets {
		required[id] = true
	}
	selected := make(map[string]selection)
	if !s.search(required, selected) {
		if s.steps >= 50000 {
			return nil, errors.New("dependency search limit reached; narrow the requested packages or sources")
		}
		return nil, fmt.Errorf("no compatible package combination: %w", s.conflict)
	}
	var result []Entry
	for _, id := range orderedKeys(selected) {
		if selected[id].entry != nil {
			result = append(result, *selected[id].entry)
		}
	}
	return result, nil
}

func (s *solver) search(required map[string]bool, selected map[string]selection) bool {
	if len(required) == len(selected) {
		return true
	}
	if s.steps >= 50000 {
		return false
	}
	s.steps++
	var next string
	var options []selection
	for _, id := range orderedKeys(required) {
		if _, assigned := selected[id]; assigned {
			continue
		}
		var compatible []selection
		for _, candidate := range s.domain(id) {
			if compatibleWith(candidate.manifest, selected) {
				compatible = append(compatible, candidate)
			}
		}
		if len(compatible) == 0 {
			s.conflict = s.domainErrors[id]
			if s.conflict == nil {
				s.conflict = fmt.Errorf("no available version satisfies the requirements for %s", id)
			}
			return false
		}
		if next == "" || s.targets[id] && !s.targets[next] ||
			s.targets[id] == s.targets[next] && len(compatible) < len(options) {
			next, options = id, compatible
		}
	}
	for _, option := range options {
		selected[next] = option
		var added []string
		require := func(id string) {
			if !required[id] {
				required[id] = true
				added = append(added, id)
			}
		}
		for id := range option.manifest.Dependencies {
			require(id)
		}
		for _, id := range option.manifest.Members {
			require(id)
		}
		for id := range option.manifest.LibrarySchemas {
			require(id)
		}
		if s.search(required, selected) {
			return true
		}
		for _, id := range added {
			delete(required, id)
		}
		delete(selected, next)
	}
	return false
}

func compatibleWith(candidate packages.Manifest, selected map[string]selection) bool {
	for _, other := range selected {
		if !accepts(candidate, other.manifest) || !accepts(other.manifest, candidate) {
			return false
		}
	}
	return true
}

func accepts(p, dependency packages.Manifest) bool {
	if version, ok := p.Dependencies[dependency.ID]; ok && version != "*" && version != dependency.Version {
		return false
	}
	if supported, ok := p.LibrarySchemas[dependency.ID]; ok && !supported.Supports(schemaOf(dependency)) {
		return false
	}
	return true
}

func orderedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
