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

// NameFor constructs the machine-readable name for an extension.
func NameFor(publisher, kind, shortName string) (string, error) {
	if !publisherRE.MatchString(publisher) {
		return "", fmt.Errorf("invalid publisher %q: use lowercase ASCII letters and digits, starting with a letter", publisher)
	}
	if len(shortName) > 48 || !shortNameRE.MatchString(shortName) {
		return "", fmt.Errorf("invalid shortName %q: use 1–48 lowercase ASCII letters and digits, with single hyphens between words", shortName)
	}
	switch kind {
	case "book", "library", "theme", "tool", "bundle":
	case "toolplugin":
		kind = "tool"
	default:
		return "", fmt.Errorf("invalid extension type %q", kind)
	}
	return publisher + "-" + kind + "-" + shortName, nil
}

// ParseName splits a canonical name and returns the manifest type.
func ParseName(name string) (publisher, kind, shortName string, err error) {
	parts := strings.SplitN(name, "-", 3)
	if len(parts) != 3 || parts[1] == "toolplugin" {
		return "", "", "", fmt.Errorf("invalid package name %q: expected publisher-type-shortName", name)
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
		return fmt.Errorf("invalid package version %q", version)
	}
	return nil
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
		return fmt.Errorf("invalid package name or summary")
	}
	if m.ShortName == "" && m.DisplayName == "" && m.Description == "" {
		// Earlier manifests used a display label in name and required summary.
		if strings.TrimSpace(m.Summary) == "" {
			return fmt.Errorf("package summary is required for a legacy manifest")
		}
		return nil
	}
	name, err := NameFor(m.Publisher, m.Type, m.ShortName)
	if err != nil {
		return err
	}
	if m.Name != name {
		return fmt.Errorf("package name %q does not match %q", m.Name, name)
	}
	if strings.TrimSpace(m.DisplayName) == "" || strings.ContainsFunc(m.DisplayName, unicode.IsControl) ||
		strings.ContainsFunc(m.Description, unicode.IsControl) {
		return fmt.Errorf("invalid package displayName or description")
	}
	return nil
}
