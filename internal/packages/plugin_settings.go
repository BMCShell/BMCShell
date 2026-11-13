package packages

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"bmcshell/internal/envregistry"
)

var pluginSettingKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type PluginSetting struct {
	Key     string                `json:"key"`
	Label   string                `json:"label"`
	Path    string                `json:"path,omitempty"`
	Hint    string                `json:"hint,omitempty"`
	Env     string                `json:"env,omitempty"`
	Type    string                `json:"type"`
	Default json.RawMessage       `json:"default"`
	Options []PluginSettingOption `json:"options,omitempty"`
	Min     *int64                `json:"min,omitempty"`
	Max     *int64                `json:"max,omitempty"`
}

type PluginSettingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
}

func (o *PluginSettingOption) UnmarshalJSON(data []byte) error {
	var option struct {
		Value *string `json:"value"`
		Label string  `json:"label"`
		Hint  string  `json:"hint,omitempty"`
	}
	if err := json.Unmarshal(data, &option); err != nil {
		return err
	}
	if option.Value == nil {
		return fmt.Errorf("枚举选项须声明字符串 value")
	}
	*o = PluginSettingOption{Value: *option.Value, Label: option.Label, Hint: option.Hint}
	return nil
}

// DefaultValue returns the default from a validated package manifest in the same
// text format used for command arguments and stored overrides.
func (s PluginSetting) DefaultValue() string {
	value, _ := s.decodeDefault()
	return value
}

func (s PluginSetting) decodeDefault() (string, error) {
	value := strings.TrimSpace(string(s.Default))
	if value == "" || value == "null" {
		return "", fmt.Errorf("须声明 default")
	}
	if s.Type == "enum" || s.Type == "string" {
		var decoded string
		if err := json.Unmarshal(s.Default, &decoded); err != nil {
			return "", fmt.Errorf("default 须为 JSON 字符串")
		}
		return decoded, nil
	}
	return value, nil
}

// Validate checks a textual setting value against the package declaration.
func (s PluginSetting) Validate(value string) error {
	if !printableSettingText(value, 1024) {
		return fmt.Errorf("设置 %q 须为最多 1024 字节的可打印 UTF-8 文本", s.Label)
	}
	switch s.Type {
	case "string":
		return nil
	case "enum":
		for _, option := range s.Options {
			if value == option.Value {
				return nil
			}
		}
		return fmt.Errorf("设置 %q 须使用清单中的选项值", s.Label)
	case "integer":
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || strconv.FormatInt(number, 10) != value {
			return fmt.Errorf("设置 %q 须为十进制整数，且无加号或前导零", s.Label)
		}
		if s.Min != nil && number < *s.Min {
			return fmt.Errorf("设置 %q 须大于或等于 %d", s.Label, *s.Min)
		}
		if s.Max != nil && number > *s.Max {
			return fmt.Errorf("设置 %q 须小于或等于 %d", s.Label, *s.Max)
		}
		return nil
	case "boolean":
		if value == "true" || value == "false" {
			return nil
		}
		return fmt.Errorf("设置 %q 须为 true 或 false", s.Label)
	default:
		return fmt.Errorf("设置 %q 的类型 %q 无效", s.Label, s.Type)
	}
}

func (m Manifest) validatePluginSettings() error {
	if len(m.Settings) > 32 {
		return fmt.Errorf("settings 最多可包含 32 项")
	}
	if len(m.Settings) != 0 && m.Type == "theme" && filepath.Base(m.Data) != ".bashrc.perso" {
		return fmt.Errorf("声明 settings 的主题须使用 .bashrc.perso 入口")
	}
	seenKeys := make(map[string]bool, len(m.Settings))
	seenEnv := make(map[string]bool, len(m.Settings))
	for _, setting := range m.Settings {
		if !pluginSettingKey.MatchString(setting.Key) || (m.Type == "theme" && setting.Key == "active") {
			return fmt.Errorf("设置键 %q 无效", setting.Key)
		}
		if seenKeys[setting.Key] {
			return fmt.Errorf("设置键 %q 重复", setting.Key)
		}
		if err := setting.validateDeclaration(); err != nil {
			return fmt.Errorf("设置 %q：%w", setting.Key, err)
		}
		seenKeys[setting.Key] = true
		if setting.Env != "" {
			if !validPluginSettingEnv(setting.Env, m.Publisher) {
				return fmt.Errorf("设置 %q 的环境变量 %q 无效或不属于发布方命名空间", setting.Key, setting.Env)
			}
			if seenEnv[setting.Env] {
				return fmt.Errorf("设置环境变量 %q 重复", setting.Env)
			}
			seenEnv[setting.Env] = true
		}
	}
	return nil
}

func (s PluginSetting) validateDeclaration() error {
	if !validSettingLabel(s.Label) {
		return fmt.Errorf("显示名称须为 1 到 120 字节的可打印 UTF-8 文本，首尾不能有空白")
	}
	if s.Path != "" {
		if len(s.Path) > 256 {
			return fmt.Errorf("path 最多为 256 字节")
		}
		for part := range strings.SplitSeq(s.Path, ".") {
			if !pluginSettingKey.MatchString(part) {
				return fmt.Errorf("path 须为点号分隔的小写标识符")
			}
		}
	}
	if !validSettingHint(s.Hint) {
		return fmt.Errorf("hint 须为最多 1024 字节的可打印单行 UTF-8 文本")
	}
	if s.Type == "enum" {
		if len(s.Options) == 0 || len(s.Options) > 32 {
			return fmt.Errorf("枚举须声明 1 到 32 个选项")
		}
		seen := make(map[string]bool, len(s.Options))
		for _, option := range s.Options {
			if !validSettingLabel(option.Label) || !printableSettingText(option.Value, 1024) || seen[option.Value] {
				return fmt.Errorf("枚举选项的名称或值无效，或值重复")
			}
			if !validSettingHint(option.Hint) {
				return fmt.Errorf("枚举选项 %q 的 hint 须为最多 1024 字节的可打印单行 UTF-8 文本", option.Label)
			}
			seen[option.Value] = true
		}
	} else if s.Options != nil {
		return fmt.Errorf("只有 enum 类型可以声明 options")
	}
	if s.Type != "integer" && (s.Min != nil || s.Max != nil) {
		return fmt.Errorf("只有 integer 类型可以声明 min 和 max")
	}
	if s.Min != nil && s.Max != nil && *s.Min > *s.Max {
		return fmt.Errorf("min 不能大于 max")
	}
	value, err := s.decodeDefault()
	if err != nil {
		return err
	}
	if err := s.Validate(value); err != nil {
		return fmt.Errorf("default 无效：%w", err)
	}
	return nil
}

func validPluginSettingEnv(name, publisher string) bool {
	if !envregistry.ValidName(name) || !publisherRE.MatchString(publisher) ||
		!strings.HasPrefix(name, strings.ToUpper(publisher)+"_") {
		return false
	}
	for _, reserved := range []string{"BASH_", "LD_", "DYLD_", "MSYS", "CYGWIN", "BMCSHELL_BRIDGE_", "BMCSHELL_BOOK_"} {
		if strings.HasPrefix(name, reserved) {
			return false
		}
	}
	switch name {
	case "BMCSHELL_CONTEXT", "BMCSHELL_ENTRY", "BMCSHELL_RESULT_FILE", "BMCSHELL_EXE", "BMCSHELL_HOME",
		"BMCSHELL_RESOLVER_LIBRARIES", "BMCSHELL_RESOLVER_BASH", "BMCSHELL_RESOLVER_CORE_EXE", "BMCSHELL_UI_PAGER":
		return false
	}
	return true
}

func validSettingLabel(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && printableSettingText(value, 120)
}

func validSettingHint(value string) bool {
	return value == "" || strings.TrimSpace(value) != "" && printableSettingText(value, 1024)
}

func printableSettingText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && strings.IndexFunc(value, func(r rune) bool { return !unicode.IsPrint(r) }) == -1
}
