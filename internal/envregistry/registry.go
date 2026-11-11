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

func ValidKey(key string) bool { return keyRE.MatchString(key) }

func (r Registry) ByKey(key string) (Field, bool) {
	for _, field := range r {
		if field.Key == key {
			return field, true
		}
	}
	return Field{}, false
}

func (f Field) Validate(value string) error {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("%s 必须为不含控制字符的 UTF-8 文本", f.Name)
	}
	switch f.Name {
	case "BMCSHELL_TARGET_IP":
		addr, err := netip.ParseAddr(value)
		if err != nil || addr.Zone() != "" {
			return fmt.Errorf("%s 必须是 IPv4 或 IPv6 地址", f.Name)
		}
	case "BMCSHELL_TLS_MODE":
		if value != "verify" && value != "insecure" {
			return fmt.Errorf("%s 必须为 verify 或 insecure", f.Name)
		}
	case "BMCSHELL_SNMP_ENTERPRISES":
		if value != "" && (len(value) > 256 || !snmpOIDRE.MatchString(value)) {
			return fmt.Errorf("%s 必须留空，或包含十进制 OID 分量", f.Name)
		}
	case "BMCSHELL_SNMP_VERSION":
		if value != "1" && value != "2c" && value != "3" {
			return fmt.Errorf("%s 必须为 1、2c 或 3", f.Name)
		}
	case "BMCSHELL_IPMI_PROXY":
		address, err := netip.ParseAddrPort(value)
		if value != "" && (err != nil || address.Addr() != netip.AddrFrom4([4]byte{127, 0, 0, 1}) || address.Port() == 0) {
			return fmt.Errorf("%s 必须留空，或使用 127.0.0.1 和非零端口", f.Name)
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
			return nil, fmt.Errorf("%s 中不能移除 %s 或更改其标识", path, name)
		}
	}
	return registry, nil
}

func (r Registry) AddPlugin(reader io.Reader, publisher string) error {
	if !publisherRE.MatchString(publisher) || publisher == "bmcshell" {
		return fmt.Errorf("插件环境变量登记表的发布方 %q 无效", publisher)
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
			return fmt.Errorf("环境变量 %s 或逻辑键 %s 重复", name, field.Key)
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
		return nil, errors.New("环境变量登记表须采用 UTF-8 编码，大小不得超过 256 KiB")
	}
	csvReader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	header, err := csvReader.Read()
	if err != nil {
		return nil, fmt.Errorf("无法读取环境变量登记表的表头：%w", err)
	}
	full := slices.Equal(header, fullHeader)
	if !full && (publisher == "" || !slices.Equal(header, pluginHeader)) {
		return nil, errors.New("环境变量登记表的 CSV 表头无效")
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
			return nil, fmt.Errorf("环境变量登记表第 %d 行：%w", line, err)
		}
		if _, exists := result[name]; exists || keys[field.Key] {
			return nil, fmt.Errorf("环境变量登记表第 %d 行中的变量 %s 或逻辑键 %s 重复", line, name, field.Key)
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
		return Field{}, fmt.Errorf("变量名 %q 不符合命名空间 %s 的要求", name, namespace)
	}
	if key == "" {
		key = name
	}
	if key != name && (!ValidKey(key) ||
		(publisher != "" && !strings.HasPrefix(key, "oem."+publisher+"."))) {
		return Field{}, fmt.Errorf("变量 %s 的逻辑键无效", name)
	}
	field := Field{Name: name, Key: key}
	switch sensitive {
	case "true", "是":
		field.Sensitive = true
	case "false", "否", "":
	default:
		return Field{}, fmt.Errorf("变量 %s 的敏感标记无效", name)
	}
	if defaultValue == "" {
		return field, nil
	}
	if field.Sensitive {
		return Field{}, fmt.Errorf("敏感变量 %s 不能在 CSV 中声明默认值", name)
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
