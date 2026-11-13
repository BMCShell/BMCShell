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
	Entry             string             `json:"entry"`
	EnvReads          []envregistry.Read `json:"envReads,omitempty"`
	ProxyCapabilities []string           `json:"proxyCapabilities,omitempty"`
	StatusFields      []StatusField      `json:"statusFields,omitempty"`
}

type StatusField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
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
	Settings           []PluginSetting               `json:"settings,omitempty"`
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
		return fmt.Errorf("包路径 %q 不符合路径要求", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		base := strings.TrimRight(strings.SplitN(part, ".", 2)[0], " ")
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") ||
			strings.EqualFold(part, originFile) ||
			strings.ContainsAny(part, `<>:"\|?*`) || strings.ContainsFunc(part, unicode.IsControl) ||
			strings.TrimRight(part, ". ") != part || dosName.MatchString(strings.ToUpper(base)) {
			return fmt.Errorf("包路径 %q 不符合路径要求", rel)
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
			return "", fmt.Errorf("包文件 %q 无效", rel)
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
		return p, fmt.Errorf("包清单超过 1 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p.Manifest); err != nil {
		return p, fmt.Errorf("%s 无效：%w", ManifestFile, err)
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
			return fmt.Errorf("包文档 %q 无效，须为 JSON 对象", rel)
		}
	}
	return nil
}

func (p Package) validate() error {
	if p.Schema != 1 || p.CoreAPI != 1 {
		return fmt.Errorf("不支持包结构版本 %d 或框架 API 版本 %d", p.Schema, p.CoreAPI)
	}
	if !packageID.MatchString(p.ID) {
		return fmt.Errorf("包 ID %q 无效", p.ID)
	}
	if err := ValidateVersion(p.Version); err != nil {
		return err
	}
	if err := p.validateIdentity(); err != nil {
		return err
	}
	if err := p.validatePluginSettings(); err != nil {
		return err
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	compatible := len(p.Platforms) == 0
	for _, supported := range p.Platforms {
		compatible = compatible || supported == "any" || supported == platform
	}
	if !compatible {
		return fmt.Errorf("包 %s 不支持平台 %s", p.ID, platform)
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
			return fmt.Errorf("依赖包 ID %q 无效", id)
		}
		if constraint != "*" && !versionRE.MatchString(constraint) {
			return fmt.Errorf("依赖版本 %q 不受支持，请使用精确版本或 *", constraint)
		}
	}
	if p.Library != nil && p.Type != "library" {
		return fmt.Errorf("包 %s 的类型不是 library，不能声明库结构", p.ID)
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
				return fmt.Errorf("业务库数据文件 %q 须以 .library.db 结尾", p.Data)
			case p.Library == nil && isSQLite:
				return fmt.Errorf("SQLite 业务库 %s 须在清单中声明库结构", p.ID)
			case p.Library != nil:
				if err := p.Library.Validate(); err != nil {
					return fmt.Errorf("业务库结构声明无效：%w", err)
				}
			}
		}
	case "tool", "toolplugin":
		for name, tool := range p.Provides.Tools {
			if err := validateProxyCapabilities(tool.ProxyCapabilities); err != nil {
				return fmt.Errorf("工具 %q：%w", name, err)
			}
			if name == "bmc" || !entryName.MatchString(name) || (tool.Scope != "global" && tool.Scope != "session") {
				return fmt.Errorf("工具 %q 的名称或作用域无效", name)
			}
			switch tool.Runtime {
			case "", "native":
				if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(tool.Entry), ".exe") {
					return fmt.Errorf("Windows 原生工具 %q 须使用 .exe 入口", name)
				}
			case "bash":
				if !strings.EqualFold(filepath.Ext(tool.Entry), ".sh") {
					return fmt.Errorf("Bash 工具 %q 须使用 .sh 入口", name)
				}
			default:
				return fmt.Errorf("运行方式 %q 不适用于工具 %q", tool.Runtime, name)
			}
			if _, err := p.File(tool.Entry); err != nil {
				return err
			}
		}
		for name, resolver := range p.Provides.Resolvers {
			if err := validateProxyCapabilities(resolver.ProxyCapabilities); err != nil {
				return fmt.Errorf("解析器 %q：%w", name, err)
			}
			if name == "generic" || !entryName.MatchString(name) {
				return fmt.Errorf("解析器名称 %q 无效", name)
			}
			if err := validateStatusFields(name, resolver.StatusFields); err != nil {
				return fmt.Errorf("解析器 %q：%w", name, err)
			}
			if _, err := p.File(resolver.Entry); err != nil {
				return err
			}
		}
	case "bundle":
		for _, member := range p.Members {
			if member == p.ID || !packageID.MatchString(member) {
				return fmt.Errorf("Bundle 成员 %q 无效", member)
			}
		}
	default:
		return fmt.Errorf("未知包类型 %q", p.Type)
	}
	if CanonicalType(p.Type) != "tool" && (len(p.Provides.Tools) > 0 || len(p.Provides.Resolvers) > 0) {
		return fmt.Errorf("包 %s 的类型不是 toolplugin，不能提供命令或解析器", p.ID)
	}
	if p.Type != "bundle" && len(p.Members) > 0 {
		return fmt.Errorf("包 %s 的类型不是 bundle，不能声明成员", p.ID)
	}
	return nil
}

func validateStatusFields(resolver string, fields []StatusField) error {
	if len(fields) > 16 {
		return fmt.Errorf("statusFields 最多可包含 16 项")
	}
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if !envregistry.ValidKey(field.Key) || seen[field.Key] ||
			(field.Key != "target.model" && field.Key != "snmp.enterprises" &&
				!strings.HasPrefix(field.Key, "oem."+resolver+".")) {
			return fmt.Errorf("状态字段 %q 未授权或重复", field.Key)
		}
		if field.Label == "" || len(field.Label) > 120 || strings.TrimSpace(field.Label) != field.Label ||
			strings.ContainsFunc(field.Label, func(r rune) bool { return !unicode.IsPrint(r) }) {
			return fmt.Errorf("状态字段 %q 的显示名称无效", field.Key)
		}
		seen[field.Key] = true
	}
	return nil
}

func validateProxyCapabilities(capabilities []string) error {
	seen := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if capability != "tcp" && capability != "udp" || seen[capability] {
			return fmt.Errorf("代理功能 %q 无效或重复", capability)
		}
		seen[capability] = true
	}
	return nil
}

func (p Package) validateLibraryLinks() error {
	if CanonicalType(p.Type) != "tool" && len(p.LibrarySchemas) > 0 {
		return fmt.Errorf("包 %s 的类型不是 toolplugin，不能声明业务库结构版本依赖", p.ID)
	}
	for id, supported := range p.LibrarySchemas {
		if !packageID.MatchString(id) || id == p.ID || supported.Min < 1 || supported.Max < supported.Min {
			return fmt.Errorf("业务库结构版本依赖 %q 无效", id)
		}
	}
	if p.Library == nil && len(p.LibraryMigrations) > 0 {
		return fmt.Errorf("包 %s 须声明库结构才能提供迁移", p.ID)
	}
	for previous, path := range p.LibraryMigrations {
		version, err := strconv.Atoi(previous)
		if err != nil || strconv.Itoa(version) != previous || version < 1 || version >= p.Library.SchemaVersion {
			return fmt.Errorf("业务库迁移的源版本 %q 无效", previous)
		}
		if !strings.EqualFold(filepath.Ext(path), ".sql") {
			return fmt.Errorf("业务库迁移文件 %q 须为 SQL 文件", path)
		}
		if _, err := p.File(path); err != nil {
			return err
		}
	}
	return nil
}
