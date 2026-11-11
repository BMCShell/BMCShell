package cli

import (
	"os"

	"bmcshell/internal/envregistry"
	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

// Book substitutions consume the shared registry while excluding credentials.
func (a *app) bookQueryEnvironment(ctx session.Context) (envregistry.Registry, map[string]string, error) {
	registry, err := a.environment()
	if err != nil {
		return nil, nil, err
	}
	config, err := settings.Load(a.home)
	if err != nil {
		return nil, nil, err
	}
	values := make(map[string]string)
	for name, field := range registry {
		if field.Sensitive {
			continue
		}
		value, present := os.LookupEnv(name)
		if name == "BMCSHELL_SNMP_ENTERPRISES" && value == "" {
			present = false
		}
		if !present {
			value, present = ctx[field.Key]
		}
		if !present {
			value, present = config.EnvDefaults[name]
		}
		if !present && field.FrameworkDefault != nil {
			value, present = *field.FrameworkDefault, true
		}
		if !present && field.PluginDefault != nil {
			value, present = *field.PluginDefault, true
		}
		if present {
			values[name] = value
		}
	}
	return registry, values, nil
}
