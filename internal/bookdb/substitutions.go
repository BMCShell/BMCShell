package bookdb

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"bmcshell/internal/envregistry"
)

const (
	enterpriseVariable    = "BMCSHELL_SNMP_ENTERPRISES"
	enterpriseOIDRoot     = "1.3.6.1.4.1."
	enterpriseOIDTemplate = enterpriseOIDRoot + "enterprises"
)

var (
	bookToken = regexp.MustCompile("^\\{\\{[a-z][a-z0-9_]{0,31}\\}\\}$")
)

type bookRendering struct {
	enterprise string
	fields     map[string]*strings.Replacer
}

func (m metadata) rendering(cid string, registry envregistry.Registry, values map[string]string) (bookRendering, error) {
	result := bookRendering{enterprise: oidPrefix(cid), fields: make(map[string]*strings.Replacer)}
	for field, option := range m.Options {
		if option.Format == "oid" {
			if value := values[enterpriseVariable]; value != "" {
				registered, exists := registry[enterpriseVariable]
				if !exists || registered.Sensitive || registered.Validate(value) != nil {
					return bookRendering{}, fmt.Errorf("登记字段 %s 的值无效", enterpriseVariable)
				}
				result.enterprise = value
				if !strings.Contains(value, ".") && strings.Trim(value, "0") != "" {
					result.enterprise = enterpriseOIDRoot + value
				}
			}
		}
		pairs := make([]string, 0, len(option.Substitutions)*2)
		for token, variable := range option.Substitutions {
			registered, exists := registry[variable]
			if !exists || registered.Sensitive {
				return bookRendering{}, fmt.Errorf("工具书只能替换已登记的公开字段，字段 %s 不符合要求", variable)
			}
			if value, present := values[variable]; present {
				if len(value) > 256 || registered.Validate(value) != nil || !validArgument(value) || strings.ContainsFunc(value, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
					return bookRendering{}, fmt.Errorf("用于工具书替换的字段 %s 值无效", variable)
				}
				pairs = append(pairs, token, value)
			}
		}
		if len(pairs) != 0 {
			result.fields[field] = strings.NewReplacer(pairs...)
		}
	}
	return result, nil
}

func canonicalEnterprisePrefix(enterprise string) string {
	if enterprise == "" || strings.Contains(enterprise, ".") {
		return enterprise
	}
	return enterpriseOIDRoot + enterprise
}

func swapOIDPrefix(value, from, to string) string {
	if from == "" {
		return value
	}
	path, absolute := strings.CutPrefix(value, ".")
	if path == from || strings.HasPrefix(path, from+".") {
		if absolute {
			return "." + to + path[len(from):]
		}
		return to + path[len(from):]
	}
	return value
}

func (r bookRendering) apply(field, format, text string) (string, error) {
	if format == "oid" && r.enterprise != "" {
		text = swapOIDPrefix(text, enterpriseOIDTemplate, canonicalEnterprisePrefix(r.enterprise))
		text = swapOIDPrefix(text, "enterprises", r.enterprise)
	}
	replacer := r.fields[field]
	if replacer == nil {
		if len(text) > bodyLimit {
			return "", errors.New("替换后工具书字段超过 2 MiB")
		}
		return text, nil
	}
	var rendered substitutedText
	if _, err := replacer.WriteString(&rendered, text); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

type substitutedText struct{ strings.Builder }

func (s *substitutedText) Write(p []byte) (int, error) {
	return s.WriteString(string(p))
}

func (s *substitutedText) WriteString(text string) (int, error) {
	if len(text) > bodyLimit-s.Len() {
		return 0, errors.New("替换后工具书字段超过 2 MiB")
	}
	return s.Builder.WriteString(text)
}
