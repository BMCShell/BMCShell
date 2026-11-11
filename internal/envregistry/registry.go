package envregistry

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"bmcshell/doc"
)

const FrameworkFilename = "ENVIRONMENT-REGISTRY-v3.csv"
const maxCSVBytes = 256 << 10

var (
	nameRE       = regexp.MustCompile(`^[A-Z][A-Z0-9]*_[A-Z][A-Z0-9]*_[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*$`)
	publisherRE  = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	snmpOIDRE    = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)
	keyRE        = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`)
	fullHeader   = []string{"逻辑字段", "现有载体", "独立环境变量", "分类", "生效范围", "是否必需", "是否敏感", "框架默认值", "状态", "说明"}
	pluginHeader = []string{"name", "key", "sensitive", "default"}
)

type Read struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

type Write struct {
	Name string `json:"name"`
}

type Field struct {
	Name             string
	Key              string
	Sensitive        bool
	FrameworkDefault *string
	PluginDefault    *string
}

type Registry map[string]Field

func ValidName(name string) bool { return nameRE.MatchString(name) }

func (f Field) Validate(value string) error {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("%s must be UTF-8 text without control characters", f.Name)
	}
	switch f.Name {
	case "BMCSHELL_TARGET_IP":
		addr, err := netip.ParseAddr(value)
		if err != nil || addr.Zone() != "" {
			return fmt.Errorf("%s must be an IPv4 or IPv6 address", f.Name)
		}
	case "BMCSHELL_TLS_MODE":
		if value != "verify" && value != "insecure" {
			return fmt.Errorf("%s must be verify or insecure", f.Name)
		}
	case "BMCSHELL_SNMP_ENTERPRISES":
		if value != "" && (len(value) > 256 || !snmpOIDRE.MatchString(value)) {
			return fmt.Errorf("%s must be empty or contain decimal OID components", f.Name)
		}
	case "BMCSHELL_SNMP_VERSION":
		if value != "1" && value != "2c" && value != "3" {
			return fmt.Errorf("%s must be 1, 2c or 3", f.Name)
		}
	case "BMCSHELL_IPMI_PROXY":
		address, err := netip.ParseAddrPort(value)
		if value != "" && (err != nil || address.Addr() != netip.AddrFrom4([4]byte{127, 0, 0, 1}) || address.Port() == 0) {
			return fmt.Errorf("%s must be empty or a 127.0.0.1 address with a nonzero port", f.Name)
		}
	}
	return nil
}

func LoadFramework(path string) (Registry, error) {
	builtin, err := parse(strings.NewReader(doc.RegistryCSV()), "")
	if err != nil || path == "" {
		return builtin, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	registry, err := parse(file, "")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for name, field := range builtin {
		edited, ok := registry[name]
		if !ok && name == "BMCSHELL_SNMP_ENTERPRISES" {
			registry[name] = field
			continue
		}
		if !ok || edited.Key != field.Key || edited.Sensitive != field.Sensitive {
			return nil, fmt.Errorf("%s: cannot remove or change the identity of %s", path, name)
		}
	}
	return registry, nil
}

func (r Registry) AddPlugin(reader io.Reader, publisher string) error {
	if !publisherRE.MatchString(publisher) || publisher == "bmcshell" {
		return fmt.Errorf("invalid publisher for plugin environment registry %q", publisher)
	}
	plugin, err := parse(reader, publisher)
	if err != nil {
		return err
	}
	keys := make(map[string]bool, len(r))
	for _, field := range r {
		keys[field.Key] = true
	}
	for name, field := range plugin {
		if _, exists := r[name]; exists || keys[field.Key] {
			return fmt.Errorf("duplicate environment registry variable %s or key %s", name, field.Key)
		}
	}
	for name, field := range plugin {
		r[name] = field
	}
	return nil
}

func parse(reader io.Reader, publisher string) (Registry, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxCSVBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCSVBytes || !utf8.Valid(data) {
		return nil, errors.New("environment registry must be UTF-8 and at most 256 KiB")
	}
	csvReader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	header, err := csvReader.Read()
	if err != nil {
		return nil, fmt.Errorf("environment registry header: %w", err)
	}
	full := slices.Equal(header, fullHeader)
	if !full && (publisher == "" || !slices.Equal(header, pluginHeader)) {
		return nil, errors.New("invalid environment registry CSV header")
	}
	result := make(Registry)
	keys := make(map[string]bool)
	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		line, _ := csvReader.FieldPos(0)
		name, key, sensitive, defaultValue := record[0], record[1], record[2], record[3]
		if full {
			name, key, sensitive, defaultValue = record[2], record[0], record[6], record[7]
			if publisher == "" && (name == "" || record[3] == "OEM 字段") {
				continue
			}
		}
		field, err := parseField(name, key, sensitive, defaultValue, publisher)
		if err != nil {
			return nil, fmt.Errorf("environment registry row %d: %w", line, err)
		}
		if _, exists := result[name]; exists || keys[field.Key] {
			return nil, fmt.Errorf("environment registry row %d: duplicate variable %s or key %s", line, name, field.Key)
		}
		result[name], keys[field.Key] = field, true
	}
}

func parseField(name, key, sensitive, defaultValue, publisher string) (Field, error) {
	namespace := "BMCSHELL_"
	if publisher != "" {
		namespace = strings.ToUpper(publisher) + "_"
	}
	if !ValidName(name) || !strings.HasPrefix(name, namespace) || strings.HasPrefix(name, "BMCSHELL_BRIDGE_") {
		return Field{}, fmt.Errorf("invalid variable name %q for namespace %s", name, namespace)
	}
	if key == "" {
		key = name
	}
	if key != name && (!keyRE.MatchString(key) ||
		(publisher != "" && !strings.HasPrefix(key, "oem."+publisher+"."))) {
		return Field{}, fmt.Errorf("invalid logical key for %s", name)
	}
	field := Field{Name: name, Key: key}
	switch sensitive {
	case "true", "是":
		field.Sensitive = true
	case "false", "否", "":
	default:
		return Field{}, fmt.Errorf("invalid sensitive flag for %s", name)
	}
	if defaultValue == "" {
		return field, nil
	}
	if field.Sensitive {
		return Field{}, fmt.Errorf("sensitive variable %s cannot have a CSV default", name)
	}
	if err := field.Validate(defaultValue); err != nil {
		return Field{}, err
	}
	if publisher == "" {
		field.FrameworkDefault = &defaultValue
	} else {
		field.PluginDefault = &defaultValue
	}
	return field, nil
}
