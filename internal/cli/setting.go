package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"bmcshell/internal/catalog"
	"bmcshell/internal/envregistry"
	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

const sourcePrefix = "plugin.source."

var frameworkSettings = []string{"use.default_resolver", "theme.active", "theme.override"}

func (a *app) setting(args []string) error {
	var reload bool
	if len(args) > 0 && args[len(args)-1] == "--reload" {
		switch {
		case len(args) == 1,
			args[0] == "--set" && len(args) >= 4,
			args[0] == "--get" && len(args) >= 3,
			(args[0] == "--reset" || args[0] == "--check") && len(args) >= 2:
			reload = true
			args = args[:len(args)-1]
		}
	}
	if reload && len(args) == 0 {
		return errors.New("请在加载了 BMCShell 的 Bash 中运行 bmc setting --reload")
	}
	err := a.runSetting(args)
	if err == nil && reload {
		fmt.Fprintln(a.errOut, "请在当前 Bash 运行 bmc setting --reload，以更新终端环境。")
	}
	return err
}

func (a *app) runSetting(args []string) error {
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if terminalInput(a.in) {
			return a.settingsMenu()
		}
		return a.listSettings(cfg)
	}
	if len(args) == 1 && args[0] == "--check" {
		return a.checkSettings(cfg)
	}
	if len(args) == 1 && args[0] == "--reset" {
		if err := settings.Update(a.home, func(cfg *settings.Config) error {
			cfg.Theme, cfg.DefaultResolver, cfg.EnvDefaults, cfg.ThemeSettings, cfg.PluginSettings = "", nil, nil, nil, nil
			cfg.ThemeOverride = false
			sources := []catalog.Source{}
			cfg.Sources = &sources
			return nil
		}); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "已恢复框架与扩展的默认设置。")
		return nil
	}
	if len(args) < 2 {
		return errors.New("设置操作缺失或参数无效。运行 bmc setting --help 查看用法")
	}
	action, key := args[0], args[1]
	if name, ok := strings.CutPrefix(key, sourcePrefix); ok {
		return a.sourceSetting(action, name, key, args[2:])
	}
	switch {
	case action == "--get" && len(args) == 2:
		value, err := a.settingValue(cfg, key)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, value)
		return nil
	case action == "--set" && len(args) == 3:
		return a.changeSetting(key, key, &args[2])
	case action == "--reset" && len(args) == 2:
		return a.changeSetting(key, key, nil)
	default:
		return errors.New("设置操作缺失或参数无效。运行 bmc setting --help 查看用法")
	}
}

// A nil value restores the default for the selected setting.
func (a *app) changeSetting(key, label string, value *string) error {
	var before, next string
	err := settings.Update(a.home, func(cfg *settings.Config) error {
		var err error
		before, err = a.settingValue(*cfg, key)
		if err != nil {
			if key != "theme.active" {
				return err
			}
			// A removed theme package must not prevent a new selection or reset.
			before = cfg.Theme
		}
		if err := a.editSetting(cfg, key, value); err != nil {
			return err
		}
		next, err = a.settingValue(*cfg, key)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s: %q → %q\n", label, before, next)
	return nil
}

func (a *app) editSetting(cfg *settings.Config, key string, value *string) error {
	if isPluginSetting(key) {
		p, field, err := a.pluginSetting(*cfg, key)
		if err != nil {
			return err
		}
		return setPluginSetting(cfg, p, field, value)
	}
	if value != nil {
		return a.setValue(cfg, key, *value)
	}
	switch key {
	case "use.default_resolver":
		cfg.DefaultResolver = nil
	case "theme.active":
		cfg.Theme = ""
	case "theme.override":
		cfg.ThemeOverride = false
	default:
		if _, err := a.settingField(key); err != nil {
			return err
		}
		delete(cfg.EnvDefaults, key)
	}
	return nil
}

func (a *app) listSettings(cfg settings.Config) error {
	registry, err := a.environment()
	if err != nil {
		return err
	}
	keys := append([]string{}, frameworkSettings...)
	for _, name := range sortedKeys(registry) {
		if !registry[name].Sensitive {
			keys = append(keys, name)
		}
	}
	for _, key := range keys {
		value, err := a.settingValue(cfg, key)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s = %q\n", key, value)
	}
	if cfg.Theme != "" {
		theme, err := a.activeTheme(cfg)
		if err != nil {
			return err
		}
		for _, field := range theme.Settings {
			fmt.Fprintf(a.out, "%s%s = %q\n", themeSettingPrefix, field.Key, themeSettingValue(cfg, theme, field))
		}
	}
	installed, err := a.store.List()
	if err != nil {
		return err
	}
	for _, p := range installed {
		for _, field := range p.Settings {
			fmt.Fprintf(a.out, "%s%s.%s = %q\n", pluginSettingPrefix, p.Name, field.Key, themeSettingValue(cfg, p, field))
		}
	}
	sources, err := catalog.Open(a.home).ConfigSources(cfg)
	if err != nil {
		return err
	}
	for _, source := range sources {
		fmt.Fprintf(a.out, "%s%s = %s%s\n", sourcePrefix, source.Name, redactSource(source.URL), sourceSettingOption(source))
	}
	return nil
}

func (a *app) checkSettings(cfg settings.Config) error {
	registry, err := a.environment()
	if err != nil {
		return err
	}
	for name, value := range cfg.EnvDefaults {
		field, ok := registry[name]
		if !ok || field.Sensitive || field.Validate(value) != nil {
			return fmt.Errorf("设置 %s 的值无效", name)
		}
	}
	if _, err := catalog.Open(a.home).ConfigSources(cfg); err != nil {
		return err
	}
	if cfg.DefaultResolver != nil {
		list, err := a.store.List()
		if err != nil {
			return err
		}
		if _, err := session.SelectResolver(list, "", cfg.DefaultResolver); err != nil {
			return err
		}
	}
	if cfg.Theme != "" {
		if _, err := a.activeTheme(cfg); err != nil {
			return err
		}
	}
	installed, err := a.store.List()
	if err != nil {
		return err
	}
	for _, p := range installed {
		for _, field := range p.Settings {
			if value, ok := pluginSettingOverride(cfg, p.ID, field.Key); ok {
				if err := field.Validate(value); err != nil {
					return fmt.Errorf("设置 %s%s.%s 无效：%w", pluginSettingPrefix, p.Name, field.Key, err)
				}
			}
		}
	}
	fmt.Fprintln(a.out, "设置检查通过。")
	return nil
}

func (a *app) settingValue(cfg settings.Config, key string) (string, error) {
	switch key {
	case "use.default_resolver":
		if cfg.DefaultResolver == nil {
			return "generic", nil
		}
		list, err := a.store.List()
		if err == nil {
			resolver, err := session.SelectResolver(list, "", cfg.DefaultResolver)
			if err == nil {
				return resolver.Selector(), nil
			}
		}
		// Keep an unavailable default visible and allow the user to reset it.
		return cfg.DefaultResolver.Export, nil
	case "theme.override":
		return strconv.FormatBool(cfg.ThemeOverride), nil
	case "theme.active":
		if cfg.Theme == "" {
			return "none", nil
		}
		p, err := a.activeTheme(cfg)
		if err != nil {
			return "", err
		}
		return p.Name, nil
	}
	if isPluginSetting(key) {
		p, field, err := a.pluginSetting(cfg, key)
		if err != nil {
			return "", err
		}
		return themeSettingValue(cfg, p, field), nil
	}
	field, err := a.settingField(key)
	if err != nil {
		return "", err
	}
	if value, ok := cfg.EnvDefaults[key]; ok {
		return value, nil
	}
	if field.FrameworkDefault != nil {
		return *field.FrameworkDefault, nil
	}
	if field.PluginDefault != nil {
		return *field.PluginDefault, nil
	}
	return "", nil
}

func (a *app) setValue(cfg *settings.Config, key, value string) error {
	switch key {
	case "theme.override":
		if value != "true" && value != "false" {
			return errors.New("theme.override 须设为 true 或 false")
		}
		cfg.ThemeOverride = value == "true"
		return nil
	case "theme.active":
		if value == "none" {
			cfg.Theme = ""
			return nil
		}
		p, err := a.findType(value, "theme")
		if err != nil {
			return err
		}
		cfg.Theme = p.ID
		return nil
	case "use.default_resolver":
		list, err := a.store.List()
		if err != nil {
			return err
		}
		// The menu carries the precise installed provider and export. CLI aliases
		// remain valid for packages with one resolver or an unambiguous export.
		var fallback *settings.Resolver
		if id, export, ok := strings.Cut(value, "/"); ok {
			fallback = &settings.Resolver{ExtensionID: id, Export: export}
			value = ""
		}
		resolver, err := session.SelectResolver(list, value, fallback)
		if err != nil {
			return err
		}
		cfg.DefaultResolver = nil
		if resolver.Name != "generic" {
			cfg.DefaultResolver = &settings.Resolver{ExtensionID: resolver.Package.ID, Export: resolver.Name}
		}
		return nil
	}
	field, err := a.settingField(key)
	if err != nil {
		return err
	}
	if err := field.Validate(value); err != nil {
		return err
	}
	if cfg.EnvDefaults == nil {
		cfg.EnvDefaults = make(map[string]string)
	}
	cfg.EnvDefaults[key] = value
	return nil
}

func (a *app) settingField(key string) (envregistry.Field, error) {
	registry, err := a.environment()
	if err != nil {
		return envregistry.Field{}, err
	}
	field, ok := registry[key]
	if !ok || field.Sensitive {
		return envregistry.Field{}, fmt.Errorf("未找到可配置的设置 %q", key)
	}
	return field, nil
}

func (a *app) sourceSetting(action, name, label string, args []string) error {
	repository := catalog.Open(a.home)
	sources, err := repository.Sources()
	if err != nil {
		return err
	}
	var before catalog.Source
	for _, source := range sources {
		if source.Name == name {
			before = source
			break
		}
	}
	var after catalog.Source
	switch {
	case action == "--get" && len(args) == 0:
		fmt.Fprintln(a.out, redactSource(before.URL))
		return nil
	case action == "--set" && (len(args) == 1 || len(args) == 2 && args[1] == "--insecure"):
		after = catalog.Source{Name: name, URL: args[0], Official: name == "official", InsecureTLS: len(args) == 2}
		if err := repository.SetSource(after); err != nil {
			return err
		}
	case action == "--reset" && len(args) == 0:
		if err := repository.RemoveSource(name); err != nil {
			return err
		}
	default:
		return errors.New("设置操作缺失或参数无效。运行 bmc setting --help 查看用法")
	}
	fmt.Fprintf(a.out, "%s: %q%s → %q%s\n", label,
		redactSource(before.URL), sourceSettingOption(before), redactSource(after.URL), sourceSettingOption(after))
	return nil
}

func sourceSettingOption(source catalog.Source) string {
	if source.InsecureTLS {
		return " --insecure"
	}
	return ""
}

func redactSource(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return ""
	}
	parsed.RawQuery, parsed.ForceQuery = "", false
	return parsed.Redacted()
}
