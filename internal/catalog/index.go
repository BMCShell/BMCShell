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
	Description          string                                 `json:"description,omitempty"`
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
	source               Source
}

// MatchesInstalled reports whether the package was installed from this source entry.
func (e Entry) MatchesInstalled(p packages.Package) bool {
	return e.ID == p.ID && e.Version == p.Version && e.Source == p.Source && e.CoreAPI == p.CoreAPI &&
		packages.CanonicalType(e.Type) == packages.CanonicalType(p.Type) && equalList(e.Platforms, p.Platforms)
}

type indexFile struct {
	Schema   int     `json:"schema"`
	Packages []Entry `json:"packages"`
}

func parseIndex(source Source, data []byte) ([]Entry, error) {
	if len(data) > maxIndexBytes || !utf8.Valid(data) {
		return nil, errors.New("软件源索引的大小或编码无效")
	}
	var index indexFile
	if err := json.Unmarshal(data, &index); err != nil || index.Schema != 1 || index.Packages == nil || len(index.Packages) > maxEntries {
		return nil, errors.New("软件源索引无效，须包含 schema 1 和 packages 数组")
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
			return nil, fmt.Errorf("软件源索引中的第 %d 个包：%w", i+1, err)
		}
		if previousType := types[entry.ID]; previousType != "" && previousType != entry.Type {
			return nil, fmt.Errorf("软件源索引中 UUID %s 的包类型冲突", entry.ID)
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
				return nil, fmt.Errorf("包 %s %s 在平台 %s 上的发布文件定义重叠", entry.ID, entry.Version, platform)
			}
			used[platform] = true
		}
		artifact, err := artifactURL(base, entry.URL)
		if err != nil {
			return nil, fmt.Errorf("软件源索引中的第 %d 个包：%w", i+1, err)
		}
		entry.URL = artifact
		entry.SHA256 = strings.ToLower(entry.SHA256)
		entry.Source = source.Name
		entry.source = source
	}
	return index.Packages, nil
}

func (e Entry) validate() error {
	if !packageID.MatchString(e.ID) {
		return errors.New("包的 UUID 无效")
	}
	if strings.TrimSpace(e.Name) == "" || len(e.Name) > 256 || strings.ContainsFunc(e.Name, unicode.IsControl) {
		return errors.New("包名无效")
	}
	switch e.Type {
	case "book", "library", "theme", "tool", "toolplugin", "bundle":
	default:
		return errors.New("包类型无效")
	}
	if _, kind, _, err := packages.ParseName(e.Name); err == nil && kind != e.Type {
		return errors.New("包名中的类型与声明的包类型不一致")
	}
	if len(e.Version) > 128 || packages.ValidateVersion(e.Version) != nil || e.CoreAPI < 1 {
		return errors.New("包版本或框架 API 版本无效")
	}
	digest, err := hex.DecodeString(e.SHA256)
	if err != nil || len(digest) != 32 {
		return errors.New("包的 SHA-256 须为 64 个十六进制字符")
	}
	seen := make(map[string]bool, len(e.Platforms))
	for _, platform := range e.Platforms {
		if !platformName.MatchString(platform) || seen[platform] {
			return errors.New("包支持的平台无效或重复")
		}
		seen[platform] = true
	}
	for id, version := range e.Dependencies {
		if id == e.ID || !packageID.MatchString(id) || version != "*" && packages.ValidateVersion(version) != nil {
			return errors.New("软件源索引中的包依赖无效")
		}
	}
	if e.Type != "bundle" && len(e.Members) > 0 {
		return errors.New("只有 bundle 可以声明成员包")
	}
	clear(seen)
	for _, id := range e.Members {
		if id == e.ID || !packageID.MatchString(id) || seen[id] {
			return errors.New("软件源索引中的 Bundle 成员无效或重复")
		}
		seen[id] = true
	}
	if e.LibrarySchemaVersion < 0 || e.LibrarySchemaVersion > math.MaxInt32 ||
		e.LibrarySchemaVersion > 0 && e.Type != "library" ||
		e.LibrarySchemaVersion == 0 && len(e.LibraryMigrations) > 0 {
		return errors.New("软件源索引中的业务库版本或迁移元数据无效")
	}
	for previous, path := range e.LibraryMigrations {
		version, err := strconv.Atoi(previous)
		if err != nil || strconv.Itoa(version) != previous || version < 1 || version >= e.LibrarySchemaVersion ||
			!strings.HasSuffix(strings.ToLower(path), ".sql") {
			return errors.New("软件源索引中的业务库迁移信息无效")
		}
	}
	if packages.CanonicalType(e.Type) != "tool" && len(e.LibrarySchemas) > 0 {
		return errors.New("只有工具包可以声明业务库结构版本依赖")
	}
	for id, supported := range e.LibrarySchemas {
		if id == e.ID || !packageID.MatchString(id) || supported.Min < 1 || supported.Max < supported.Min {
			return errors.New("软件源索引中的业务库结构版本依赖无效")
		}
	}
	return nil
}

func artifactURL(base *url.URL, raw string) (string, error) {
	if raw == "" || len(raw) > maxURLLength {
		return "", errors.New("包的下载地址无效")
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("包的下载地址无效")
	}
	resolved, err := networkURL(base.ResolveReference(reference).String())
	if err != nil || base.Scheme == "https" && resolved.Scheme != "https" {
		return "", errors.New("包的下载地址无效")
	}
	return resolved.String(), nil
}
