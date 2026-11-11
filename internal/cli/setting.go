package cli

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"bmcshell/internal/catalog"
	"bmcshell/internal/envregistry"
	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

const sourcePrefix = "plugin.source."

var frameworkSettings = []string{"use.default_resolver", "theme.active"}

func (a *app) setting(args []string) error {
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return a.settingsMenu(cfg)
	}
	if len(args) == 1 && args[0] == "--check" {
		return a.checkSettings(cfg)
	}
	if len(args) == 1 && args[0] == "--reset" {
		cfg.Theme, cfg.DefaultResolver, cfg.EnvDefaults = "", nil, nil
		if err := settings.Save(a.home, cfg); err != nil {
			return err
		}
		if err := catalog.Open(a.home).ResetSources(); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "Framework and plugin settings reset.")
		return nil
	}
	if len(args) < 2 {
		return errors.New("see bmc setting --help")
	}
	action, key := args[0], args[1]
	if name, ok := strings.CutPrefix(key, sourcePrefix); ok {
		return a.sourceSetting(action, name, args[2:])
	}
	value, err := a.settingValue(cfg, key)
	if err != nil {
		return err
	}
	switch {
	case action == "--get" && len(args) == 2:
		fmt.Fprintln(a.out, value)
		return nil
	case action == "--set" && len(args) == 3:
		if err := a.setValue(&cfg, key, args[2]); err != nil {
			return err
		}
	case action == "--reset" && len(args) == 2:
		switch key {
		case "use.default_resolver":
			cfg.DefaultResolver = nil
		case "theme.active":
			cfg.Theme = ""
		default:
			delete(cfg.EnvDefaults, key)
		}
	default:
		return errors.New("see bmc setting --help")
	}
	next, err := a.settingValue(cfg, key)
	if err != nil {
		return err
	}
	if err := settings.Save(a.home, cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s: %q → %q\n", key, value, next)
	return nil
}

func (a *app) settingsMenu(cfg settings.Config) error {
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
	sources, err := catalog.Open(a.home).Sources()
	if err != nil {
		return err
	}
	for _, source := range sources {
		fmt.Fprintf(a.out, "%s%s = %s%s\n", sourcePrefix, source.Name, redactSource(source.URL), sourceSettingOption(source))
	}
	if !terminalInput(a.in) {
		return nil
	}
	input := bufio.NewReader(a.in)
	fmt.Fprint(a.out, "[1] 修改设置  [2] 恢复单项默认  [Enter] 返回: ")
	switch readChoice(input) {
	case "1":
		fmt.Fprintf(a.out, "设置名称（软件源使用 %s<名称>）: ", sourcePrefix)
		key, _ := input.ReadString('\n')
		fmt.Fprint(a.out, "新值: ")
		value, _ := input.ReadString('\n')
		return a.setting([]string{"--set", strings.TrimSpace(key), strings.TrimRight(value, "\r\n")})
	case "2":
		fmt.Fprint(a.out, "设置名称: ")
		key, _ := input.ReadString('\n')
		return a.setting([]string{"--reset", strings.TrimSpace(key)})
	case "":
		return nil
	default:
		return errors.New("invalid setting selection")
	}
}

func (a *app) checkSettings(cfg settings.Config) error {
	registry, err := a.environment()
	if err != nil {
		return err
	}
	for name, value := range cfg.EnvDefaults {
		field, ok := registry[name]
		if !ok || field.Sensitive || field.Validate(value) != nil {
			return fmt.Errorf("invalid value for setting %s", name)
		}
	}
	if _, err := catalog.Open(a.home).Sources(); err != nil {
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
		p, err := a.store.Find(cfg.Theme)
		if err != nil {
			return err
		}
		if p.Type != "theme" {
			return errors.New("active theme does not reference a theme package")
		}
	}
	fmt.Fprintln(a.out, "Settings are valid.")
	return nil
}

func (a *app) settingValue(cfg settings.Config, key string) (string, error) {
	switch key {
	case "use.default_resolver":
		if cfg.DefaultResolver == nil {
			return "generic", nil
		}
		return cfg.DefaultResolver.Export, nil
	case "theme.active":
		if cfg.Theme == "" {
			return "none", nil
		}
		p, err := a.store.Find(cfg.Theme)
		if err != nil {
			return "", err
		}
		return p.Name, nil
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
		resolver, err := session.SelectResolver(list, value, nil)
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
		return envregistry.Field{}, fmt.Errorf("unknown configurable setting %q", key)
	}
	return field, nil
}

func (a *app) sourceSetting(action, name string, args []string) error {
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
		return errors.New("see bmc setting --help")
	}
	fmt.Fprintf(a.out, "%s%s: %q%s → %q%s\n", sourcePrefix, name,
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
