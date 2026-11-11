package packages

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"bmcshell/internal/envregistry"
	"bmcshell/internal/librarydb"
)

const ManifestFile = "bmc-package.json"

var (
	packageID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	entryName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	dosName   = regexp.MustCompile(`^(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|COM[1-9¹²³]|LPT[1-9¹²³])$`)
)

type Tool struct {
	Scope             string              `json:"scope"`
	Entry             string              `json:"entry"`
	Runtime           string              `json:"runtime"`
	Requires          []string            `json:"requires"`
	EnvReads          []envregistry.Read  `json:"envReads,omitempty"`
	EnvWrites         []envregistry.Write `json:"envWrites,omitempty"`
	ProxyCapabilities []string            `json:"proxyCapabilities,omitempty"`
}

func (t Tool) ManagedEnv() bool {
	return t.EnvReads != nil || t.EnvWrites != nil
}

type Resolver struct {
	Entry             string   `json:"entry"`
	ProxyCapabilities []string `json:"proxyCapabilities,omitempty"`
}

type Provides struct {
	Tools     map[string]Tool     `json:"tools"`
	Resolvers map[string]Resolver `json:"resolvers"`
}

type SystemDependency struct {
	ID         string   `json:"id"`
	MinVersion string   `json:"minVersion"`
	Features   []string `json:"features"`
	RequiredBy []string `json:"requiredBy"`
}

type LibrarySchemaRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

func (r LibrarySchemaRange) Supports(version int) bool {
	return r.Min <= version && version <= r.Max
}

type Manifest struct {
	Schema             int                           `json:"schema"`
	ID                 string                        `json:"id"`
	Type               string                        `json:"type"`
	Name               string                        `json:"name"`
	Publisher          string                        `json:"publisher,omitempty"`
	ShortName          string                        `json:"shortName,omitempty"`
	DisplayName        string                        `json:"displayName,omitempty"`
	Description        string                        `json:"description,omitempty"`
	EnvRegistry        string                        `json:"envRegistry,omitempty"`
	Summary            string                        `json:"summary"`
	Version            string                        `json:"version"`
	License            string                        `json:"license"`
	Maintainers        []string                      `json:"maintainers"`
	Repository         string                        `json:"repository"`
	CoreAPI            int                           `json:"coreApi"`
	Platforms          []string                      `json:"platforms"`
	Help               string                        `json:"help"`
	Changelog          string                        `json:"changelog"`
	Data               string                        `json:"data"`
	Library            *librarydb.Contract           `json:"library,omitempty"`
	LibrarySchemas     map[string]LibrarySchemaRange `json:"librarySchemas,omitempty"`
	LibraryMigrations  map[string]string             `json:"libraryMigrations,omitempty"`
	Assets             []string                      `json:"assets"`
	Provides           Provides                      `json:"provides"`
	Members            []string                      `json:"members"`
	Dependencies       map[string]string             `json:"dependencies"`
	RepairSuggestions  string                        `json:"repairSuggestions,omitempty"`
	SystemDependencies []SystemDependency            `json:"systemDependencies"`
}

type Package struct {
	Manifest
	Root   string
	Source string
}

func validatePath(rel string) error {
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return fmt.Errorf("unsafe package path %q", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		base := strings.TrimRight(strings.SplitN(part, ".", 2)[0], " ")
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") ||
			strings.EqualFold(part, originFile) ||
			strings.ContainsAny(part, `<>:"\|?*`) || strings.ContainsFunc(part, unicode.IsControl) ||
			strings.TrimRight(part, ". ") != part || dosName.MatchString(strings.ToUpper(base)) {
			return fmt.Errorf("unsafe package path %q", rel)
		}
	}
	return nil
}

// File resolves a manifest path and accepts only regular files inside the package.
func (p Package) File(rel string) (string, error) {
	if err := validatePath(rel); err != nil {
		return "", err
	}
	path := p.Root
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return "", fmt.Errorf("invalid package file %q", rel)
		}
	}
	return path, nil
}

func loadPackage(root string) (Package, error) {
	p := Package{Root: root}
	path, err := p.File(ManifestFile)
	if err != nil {
		return p, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return p, err
	}
	if info.Size() > 1<<20 {
		return p, fmt.Errorf("manifest exceeds 1 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p.Manifest); err != nil {
		return p, fmt.Errorf("invalid %s: %w", ManifestFile, err)
	}
	return p, p.validate()
}

func (p Package) validateDocumentation() error {
	if _, err := p.RepairRecords(); err != nil {
		return err
	}
	for _, rel := range []string{p.Help, p.Changelog} {
		path, err := p.File(rel)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return fmt.Errorf("invalid package documentation %q: expected a JSON object", rel)
		}
	}
	return nil
}

func (p Package) validate() error {
	if p.Schema != 1 || p.CoreAPI != 1 {
		return fmt.Errorf("unsupported package schema or core API: %d/%d", p.Schema, p.CoreAPI)
	}
	if !packageID.MatchString(p.ID) {
		return fmt.Errorf("invalid package ID %q", p.ID)
	}
	if err := ValidateVersion(p.Version); err != nil {
		return err
	}
	if err := p.validateIdentity(); err != nil {
		return err
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	compatible := len(p.Platforms) == 0
	for _, supported := range p.Platforms {
		compatible = compatible || supported == "any" || supported == platform
	}
	if !compatible {
		return fmt.Errorf("package %s does not support %s", p.ID, platform)
	}
	for _, rel := range []string{p.Help, p.Changelog} {
		if _, err := p.File(rel); err != nil {
			return err
		}
	}
	for _, rel := range p.Assets {
		if _, err := p.File(rel); err != nil {
			return err
		}
	}
	for id, constraint := range p.Dependencies {
		if id == p.ID || !packageID.MatchString(id) {
			return fmt.Errorf("invalid dependency ID %q", id)
		}
		if constraint != "*" && !versionRE.MatchString(constraint) {
			return fmt.Errorf("unsupported dependency version %q: use an exact version or *", constraint)
		}
	}
	if p.Library != nil && p.Type != "library" {
		return fmt.Errorf("package %s: only library packages can declare a Library contract", p.ID)
	}
	if err := p.validateLibraryLinks(); err != nil {
		return err
	}
	switch p.Type {
	case "book", "library", "theme":
		if _, err := p.File(p.Data); err != nil {
			return err
		}
		if p.Type == "library" {
			isSQLite := strings.HasSuffix(strings.ToLower(p.Data), ".library.db")
			switch {
			case p.Library != nil && !isSQLite:
				return fmt.Errorf("Library data file must end in .library.db: %q", p.Data)
			case p.Library == nil && isSQLite:
				return fmt.Errorf("SQLite Library %s must declare its library contract", p.ID)
			case p.Library != nil:
				if err := p.Library.Validate(); err != nil {
					return fmt.Errorf("invalid Library contract: %w", err)
				}
			}
		}
	case "tool", "toolplugin":
		for name, tool := range p.Provides.Tools {
			if err := validateProxyCapabilities(tool.ProxyCapabilities); err != nil {
				return fmt.Errorf("tool %q: %w", name, err)
			}
			if name == "bmc" || !entryName.MatchString(name) || (tool.Scope != "global" && tool.Scope != "session") {
				return fmt.Errorf("invalid tool name or scope: %q", name)
			}
			switch tool.Runtime {
			case "", "native":
				if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(tool.Entry), ".exe") {
					return fmt.Errorf("native Windows tool %q needs an .exe entry", name)
				}
			case "bash":
				if !strings.EqualFold(filepath.Ext(tool.Entry), ".sh") {
					return fmt.Errorf("Bash tool %q needs an .sh entry", name)
				}
			default:
				return fmt.Errorf("invalid runtime %q for tool %q", tool.Runtime, name)
			}
			if _, err := p.File(tool.Entry); err != nil {
				return err
			}
		}
		for name, resolver := range p.Provides.Resolvers {
			if err := validateProxyCapabilities(resolver.ProxyCapabilities); err != nil {
				return fmt.Errorf("resolver %q: %w", name, err)
			}
			if name == "generic" || !entryName.MatchString(name) {
				return fmt.Errorf("invalid resolver name %q", name)
			}
			if _, err := p.File(resolver.Entry); err != nil {
				return err
			}
		}
	case "bundle":
		for _, member := range p.Members {
			if member == p.ID || !packageID.MatchString(member) {
				return fmt.Errorf("invalid bundle member %q", member)
			}
		}
	default:
		return fmt.Errorf("unknown package type %q", p.Type)
	}
	if CanonicalType(p.Type) != "tool" && (len(p.Provides.Tools) > 0 || len(p.Provides.Resolvers) > 0) {
		return fmt.Errorf("package %s: only toolplugins can provide commands or resolvers", p.ID)
	}
	if p.Type != "bundle" && len(p.Members) > 0 {
		return fmt.Errorf("package %s: only bundles can declare members", p.ID)
	}
	return nil
}

func validateProxyCapabilities(capabilities []string) error {
	seen := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if capability != "tcp" && capability != "udp" || seen[capability] {
			return fmt.Errorf("invalid or repeated proxy capability %q", capability)
		}
		seen[capability] = true
	}
	return nil
}

func (p Package) validateLibraryLinks() error {
	if CanonicalType(p.Type) != "tool" && len(p.LibrarySchemas) > 0 {
		return fmt.Errorf("package %s: only toolplugins can declare Library schema dependencies", p.ID)
	}
	for id, supported := range p.LibrarySchemas {
		if !packageID.MatchString(id) || id == p.ID || supported.Min < 1 || supported.Max < supported.Min {
			return fmt.Errorf("invalid Library schema dependency %q", id)
		}
	}
	if p.Library == nil && len(p.LibraryMigrations) > 0 {
		return fmt.Errorf("package %s: migrations require a Library contract", p.ID)
	}
	for previous, path := range p.LibraryMigrations {
		version, err := strconv.Atoi(previous)
		if err != nil || strconv.Itoa(version) != previous || version < 1 || version >= p.Library.SchemaVersion {
			return fmt.Errorf("invalid Library migration source version %q", previous)
		}
		if !strings.EqualFold(filepath.Ext(path), ".sql") {
			return fmt.Errorf("Library migration %q must be a SQL file", path)
		}
		if _, err := p.File(path); err != nil {
			return err
		}
	}
	return nil
}
