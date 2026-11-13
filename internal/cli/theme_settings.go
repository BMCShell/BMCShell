package cli

import (
	"errors"
	"fmt"
	"strings"

	"bmcshell/internal/packages"
	"bmcshell/internal/settings"
)

const themeSettingPrefix = "theme."
const pluginSettingPrefix = "plugin."

func themeSettingKey(key string) (string, bool) {
	name, ok := strings.CutPrefix(key, themeSettingPrefix)
	return name, ok && key != "theme.active" && key != "theme.override"
}

func (a *app) activeTheme(cfg settings.Config) (packages.Package, error) {
	if cfg.Theme == "" {
		return packages.Package{}, errors.New("尚未启用终端主题")
	}
	theme, err := a.store.Find(cfg.Theme)
	if err != nil {
		return theme, err
	}
	if theme.Type != "theme" {
		return theme, errors.New("当前主题设置引用的包不是主题包")
	}
	return theme, nil
}

func (a *app) findThemeSetting(cfg settings.Config, name string) (packages.Package, packages.PluginSetting, error) {
	theme, err := a.activeTheme(cfg)
	if err != nil {
		return theme, packages.PluginSetting{}, err
	}
	for _, field := range theme.Settings {
		if field.Key == name {
			return theme, field, nil
		}
	}
	return theme, packages.PluginSetting{}, fmt.Errorf("当前主题未声明设置 %q", themeSettingPrefix+name)
}

func themeSettingValue(cfg settings.Config, theme packages.Package, field packages.PluginSetting) string {
	if value, ok := pluginSettingOverride(cfg, theme.ID, field.Key); ok && field.Validate(value) == nil {
		return value
	}
	return field.DefaultValue()
}

func pluginSettingOverride(cfg settings.Config, id, key string) (string, bool) {
	if value, ok := cfg.PluginSettings[id][key]; ok {
		return value, true
	}
	value, ok := cfg.ThemeSettings[id][key]
	return value, ok
}

func packageSettingKey(p packages.Package, field packages.PluginSetting) string {
	return pluginSettingPrefix + p.ID + "." + field.Key
}

func (a *app) pluginSetting(cfg settings.Config, key string) (packages.Package, packages.PluginSetting, error) {
	if name, ok := themeSettingKey(key); ok {
		return a.findThemeSetting(cfg, name)
	}
	suffix := strings.TrimPrefix(key, pluginSettingPrefix)
	dot := strings.LastIndexByte(suffix, '.')
	if dot <= 0 || dot == len(suffix)-1 {
		return packages.Package{}, packages.PluginSetting{}, fmt.Errorf("插件设置 %q 须指定包名或 UUID 及参数键", key)
	}
	p, err := a.store.Find(suffix[:dot])
	if err != nil {
		return p, packages.PluginSetting{}, err
	}
	for _, field := range p.Settings {
		if field.Key == suffix[dot+1:] {
			return p, field, nil
		}
	}
	return p, packages.PluginSetting{}, fmt.Errorf("插件未声明设置 %q", key)
}

func isPluginSetting(key string) bool {
	_, theme := themeSettingKey(key)
	return theme || strings.HasPrefix(key, pluginSettingPrefix)
}

func setPluginSetting(cfg *settings.Config, p packages.Package, field packages.PluginSetting, value *string) error {
	if value != nil {
		if err := field.Validate(*value); err != nil {
			return err
		}
	}
	for _, overrides := range []map[string]map[string]string{cfg.PluginSettings, cfg.ThemeSettings} {
		delete(overrides[p.ID], field.Key)
		if len(overrides[p.ID]) == 0 {
			delete(overrides, p.ID)
		}
	}
	if value == nil {
		return nil
	}
	if cfg.PluginSettings == nil {
		cfg.PluginSettings = make(map[string]map[string]string)
	}
	if cfg.PluginSettings[p.ID] == nil {
		cfg.PluginSettings[p.ID] = make(map[string]string)
	}
	cfg.PluginSettings[p.ID][field.Key] = *value
	return nil
}

func (a *app) pluginSettingEnvironment(cfg settings.Config) ([][2]string, error) {
	installed, err := a.store.List()
	if err != nil {
		return nil, err
	}
	if _, err := a.store.Environment(installed); err != nil {
		return nil, err
	}
	var environment [][2]string
	for _, p := range installed {
		for _, field := range p.Settings {
			if field.Env != "" {
				environment = append(environment, [2]string{field.Env, themeSettingValue(cfg, p, field)})
			}
		}
	}
	return environment, nil
}
