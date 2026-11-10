package catalog

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"bmcshell/internal/packages"
)

const maxEntries = 20000

var (
	packageID    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	platformName = regexp.MustCompile(`^(?:any|[a-z0-9]+-[a-z0-9]+)$`)
)

type Entry struct {
	ID                   string                                 `json:"id"`
	Name                 string                                 `json:"name"`
	Type                 string                                 `json:"type"`
	Version              string                                 `json:"version"`
	CoreAPI              int                                    `json:"coreApi"`
	Platforms            []string                               `json:"platforms,omitempty"`
	URL                  string                                 `json:"url"`
	SHA256               string                                 `json:"sha256"`
	Dependencies         map[string]string                      `json:"dependencies,omitempty"`
	Members              []string                               `json:"members,omitempty"`
	LibrarySchemaVersion int                                    `json:"librarySchemaVersion,omitempty"`
	LibrarySchemas       map[string]packages.LibrarySchemaRange `json:"librarySchemas,omitempty"`
	LibraryMigrations    map[string]string                      `json:"libraryMigrations,omitempty"`
	Source               string                                 `json:"-"`
}

type indexFile struct {
	Schema   int     `json:"schema"`
	Packages []Entry `json:"packages"`
}

func parseIndex(source Source, data []byte) ([]Entry, error) {
	if len(data) > maxIndexBytes || !utf8.Valid(data) {
		return nil, errors.New("invalid software source index size or encoding")
	}
	var index indexFile
	if err := json.Unmarshal(data, &index); err != nil || index.Schema != 1 || index.Packages == nil || len(index.Packages) > maxEntries {
		return nil, errors.New("invalid software source index (expected schema 1 and a packages array)")
	}
	base, err := networkURL(source.URL)
	if err != nil {
		return nil, err
	}
	types := make(map[string]string)
	type release struct {
		id, version string
		api         int
	}
	variants := make(map[release]map[string]bool)
	for i := range index.Packages {
		entry := &index.Packages[i]
		if err := entry.validate(); err != nil {
			return nil, fmt.Errorf("index package %d: %w", i+1, err)
		}
		if previousType := types[entry.ID]; previousType != "" && previousType != entry.Type {
			return nil, fmt.Errorf("conflicting types for indexed UUID %s", entry.ID)
		}
		types[entry.ID] = entry.Type
		key := release{entry.ID, entry.Version, entry.CoreAPI}
		platforms := entry.Platforms
		if len(platforms) == 0 || slices.Contains(platforms, "any") {
			platforms = []string{"any"}
		}
		used := variants[key]
		if used == nil {
			used = make(map[string]bool)
			variants[key] = used
		}
		for _, platform := range platforms {
			if used[platform] || used["any"] || platform == "any" && len(used) > 0 {
				return nil, fmt.Errorf("overlapping artifacts for %s %s on %s", entry.ID, entry.Version, platform)
			}
			used[platform] = true
		}
		artifact, err := artifactURL(base, entry.URL)
		if err != nil {
			return nil, fmt.Errorf("index package %d: %w", i+1, err)
		}
		entry.URL = artifact
		entry.SHA256 = strings.ToLower(entry.SHA256)
		entry.Source = source.Name
	}
	return index.Packages, nil
}

func (e Entry) validate() error {
	if !packageID.MatchString(e.ID) {
		return errors.New("invalid package UUID")
	}
	if strings.TrimSpace(e.Name) == "" || len(e.Name) > 256 || strings.ContainsFunc(e.Name, unicode.IsControl) {
		return errors.New("invalid package name")
	}
	switch e.Type {
	case "book", "library", "theme", "toolplugin", "bundle":
	default:
		return errors.New("invalid package type")
	}
	if _, kind, _, err := packages.ParseName(e.Name); err == nil && kind != e.Type {
		return errors.New("package name and type disagree")
	}
	if len(e.Version) > 128 || packages.ValidateVersion(e.Version) != nil || e.CoreAPI < 1 {
		return errors.New("invalid package version or core API")
	}
	digest, err := hex.DecodeString(e.SHA256)
	if err != nil || len(digest) != 32 {
		return errors.New("package SHA256 must contain 64 hexadecimal characters")
	}
	seen := make(map[string]bool, len(e.Platforms))
	for _, platform := range e.Platforms {
		if !platformName.MatchString(platform) || seen[platform] {
			return errors.New("invalid or repeated package platform")
		}
		seen[platform] = true
	}
	for id, version := range e.Dependencies {
		if id == e.ID || !packageID.MatchString(id) || version != "*" && packages.ValidateVersion(version) != nil {
			return errors.New("invalid indexed package dependency")
		}
	}
	if e.Type != "bundle" && len(e.Members) > 0 {
		return errors.New("only bundles can have members")
	}
	clear(seen)
	for _, id := range e.Members {
		if id == e.ID || !packageID.MatchString(id) || seen[id] {
			return errors.New("invalid or repeated indexed bundle member")
		}
		seen[id] = true
	}
	if e.LibrarySchemaVersion < 0 || e.LibrarySchemaVersion > math.MaxInt32 ||
		e.LibrarySchemaVersion > 0 && e.Type != "library" ||
		e.LibrarySchemaVersion == 0 && len(e.LibraryMigrations) > 0 {
		return errors.New("invalid indexed Library version or migration metadata")
	}
	for previous, path := range e.LibraryMigrations {
		version, err := strconv.Atoi(previous)
		if err != nil || strconv.Itoa(version) != previous || version < 1 || version >= e.LibrarySchemaVersion ||
			!strings.HasSuffix(strings.ToLower(path), ".sql") {
			return errors.New("invalid indexed Library migration")
		}
	}
	if e.Type != "toolplugin" && len(e.LibrarySchemas) > 0 {
		return errors.New("only tools can have Library schema dependencies")
	}
	for id, supported := range e.LibrarySchemas {
		if id == e.ID || !packageID.MatchString(id) || supported.Min < 1 || supported.Max < supported.Min {
			return errors.New("invalid indexed Library schema dependency")
		}
	}
	return nil
}

func artifactURL(base *url.URL, raw string) (string, error) {
	if raw == "" || len(raw) > maxURLLength {
		return "", errors.New("invalid package artifact URL")
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("invalid package artifact URL")
	}
	resolved, err := networkURL(base.ResolveReference(reference).String())
	if err != nil || base.Scheme == "https" && resolved.Scheme != "https" {
		return "", errors.New("invalid package artifact URL")
	}
	return resolved.String(), nil
}
