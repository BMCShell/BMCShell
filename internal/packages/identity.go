package packages

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var (
	publisherRE = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	shortNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
)

// CanonicalType maps the earlier tool manifest spelling to the public type.
func CanonicalType(kind string) string {
	if kind == "toolplugin" {
		return "tool"
	}
	return kind
}

func ValidType(kind string) bool {
	switch kind {
	case "book", "library", "theme", "tool", "bundle":
		return true
	default:
		return false
	}
}

// NameFor constructs the machine-readable name for an extension.
func NameFor(publisher, kind, shortName string) (string, error) {
	if !publisherRE.MatchString(publisher) {
		return "", fmt.Errorf("发布方 %q 无效。请以小写英文字母开头，后续可含小写字母和数字", publisher)
	}
	if len(shortName) > 48 || !shortNameRE.MatchString(shortName) {
		return "", fmt.Errorf("短名 %q 无效。请使用 1 到 48 个小写英文字母和数字，以小写字母开头，单词间可用单个连字符", shortName)
	}
	switch kind {
	case "book", "library", "theme", "tool", "bundle":
	case "toolplugin":
		kind = "tool"
	default:
		return "", fmt.Errorf("插件类型 %q 无效", kind)
	}
	return publisher + "-" + kind + "-" + shortName, nil
}

// ParseName splits a canonical name and returns the manifest type.
func ParseName(name string) (publisher, kind, shortName string, err error) {
	parts := strings.SplitN(name, "-", 3)
	if len(parts) != 3 || parts[1] == "toolplugin" {
		return "", "", "", fmt.Errorf("包名 %q 无效，须使用 发布方-类型-短名 格式", name)
	}
	publisher, kind, shortName = parts[0], parts[1], parts[2]
	if _, err = NameFor(publisher, kind, shortName); err != nil {
		return "", "", "", err
	}
	if kind == "tool" {
		kind = "toolplugin"
	}
	return publisher, kind, shortName, nil
}

func ValidateVersion(version string) error {
	if !versionRE.MatchString(version) {
		return fmt.Errorf("包版本 %q 无效", version)
	}
	return nil
}

// DataName is the short lookup name for a Book or Library. Older packages
// used the full manifest name and remain readable.
func (m Manifest) DataName() string {
	if m.ShortName != "" {
		return m.ShortName
	}
	return m.Name
}

func (m Manifest) Title() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Name
}

func (m Manifest) Details() string {
	if m.Description != "" {
		return m.Description
	}
	return m.Summary
}

func (m Manifest) validateIdentity() error {
	if strings.TrimSpace(m.Name) == "" || strings.ContainsFunc(m.Name, unicode.IsControl) ||
		strings.ContainsFunc(m.Summary, unicode.IsControl) {
		return fmt.Errorf("包名或简介无效")
	}
	if m.ShortName == "" && m.DisplayName == "" && m.Description == "" {
		// Earlier manifests used a display label in name and required summary.
		if strings.TrimSpace(m.Summary) == "" {
			return fmt.Errorf("旧版包清单须提供简介")
		}
		return nil
	}
	name, err := NameFor(m.Publisher, m.Type, m.ShortName)
	if err != nil {
		return err
	}
	if m.Name != name {
		return fmt.Errorf("包名 %q 与 %q 不一致", m.Name, name)
	}
	if strings.TrimSpace(m.DisplayName) == "" || strings.ContainsFunc(m.DisplayName, unicode.IsControl) ||
		strings.ContainsFunc(m.Description, unicode.IsControl) {
		return fmt.Errorf("包的 displayName 或 description 无效")
	}
	return nil
}
