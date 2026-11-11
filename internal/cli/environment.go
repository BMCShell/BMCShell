package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"bmcshell/internal/envregistry"
	"bmcshell/internal/packages"
	"bmcshell/internal/proxy"
	"bmcshell/internal/session"
)

var shellRuntimeValues = map[string]string{
	"BMCSHELL_AUTH_USERNAME":    "BMCSHELL_BRIDGE_USERNAME",
	"BMCSHELL_AUTH_PASSWORD":    "BMCSHELL_BRIDGE_PASSWORD",
	"BMCSHELL_SNMP_COMMUNITY":   "BMCSHELL_BRIDGE_SNMP_COMMUNITY",
	"BMCSHELL_SNMP_VERSION":     "BMCSHELL_BRIDGE_SNMP_VERSION",
	"BMCSHELL_SNMP_ENTERPRISES": "BMCSHELL_SNMP_ENTERPRISES",
	"BMCSHELL_TLS_MODE":         "BMCSHELL_BRIDGE_TLS",
}

func (a *app) environment() (envregistry.Registry, error) {
	list, err := a.store.List()
	if err != nil {
		return nil, err
	}
	return a.store.Environment(list)
}

var shellStatusValues = map[string]string{
	"BMCSHELL_AUTH_USERNAME":    "BMCSHELL_BRIDGE_STATUS_USER",
	"BMCSHELL_AUTH_PASSWORD":    "BMCSHELL_BRIDGE_STATUS_PASSWORD",
	"BMCSHELL_SNMP_COMMUNITY":   "BMCSHELL_BRIDGE_STATUS_SNMP_COMMUNITY",
	"BMCSHELL_SNMP_VERSION":     "BMCSHELL_BRIDGE_STATUS_SNMP_VERSION",
	"BMCSHELL_SNMP_ENTERPRISES": "BMCSHELL_SNMP_ENTERPRISES",
	"BMCSHELL_TLS_MODE":         "BMCSHELL_BRIDGE_STATUS_TLS",
}

func (a *app) statusEnvironment(ctx session.Context) error {
	registry, err := a.environment()
	if err != nil {
		return err
	}
	address, err := a.proxyAddress()
	if err != nil {
		return err
	}
	for _, name := range sortedKeys(registry) {
		field := registry[name]
		value, ok := ctx[field.Key]
		if source := shellStatusValues[name]; source != "" {
			if runtimeValue := os.Getenv(source); runtimeValue != "" {
				value, ok = runtimeValue, true
			}
		}
		if !ok {
			continue
		}
		if field.Sensitive {
			value = "[redacted]"
		} else if field.Validate(value) != nil {
			return fmt.Errorf("invalid value for %s", name)
		}
		fmt.Fprintf(a.out, "%s = %s\n", name, value)
	}
	if address != "" {
		fmt.Fprintf(a.out, "%s = %s\n", proxy.EnvName, address)
	}
	return nil
}

func environmentValues(t packages.Tool, registry envregistry.Registry, ctx session.Context, defaults map[string]string) (map[string]string, error) {
	return environmentValuesFrom(t, registry, ctx, defaults, shellRuntimeValues)
}

func environmentValuesFrom(t packages.Tool, registry envregistry.Registry, ctx session.Context, defaults, runtime map[string]string) (map[string]string, error) {
	values := make(map[string]string, len(t.EnvReads))
	for _, read := range t.EnvReads {
		field, registered := registry[read.Name]
		if !registered {
			return nil, fmt.Errorf("unregistered environment variable %q", read.Name)
		}
		value, ok := ctx[field.Key]
		if field.Sensitive {
			value, ok = "", false
		}
		if source := runtime[read.Name]; source != "" {
			if runtimeValue := os.Getenv(source); runtimeValue != "" {
				value, ok = runtimeValue, true
			}
		}
		if !ok && !field.Sensitive {
			value, ok = defaults[read.Name]
		}
		if !ok && !field.Sensitive && field.FrameworkDefault != nil {
			value, ok = *field.FrameworkDefault, true
		}
		if !ok && !field.Sensitive && field.PluginDefault != nil {
			value, ok = *field.PluginDefault, true
		}
		if !ok {
			if read.Required {
				return nil, fmt.Errorf("environment variable %s is required", read.Name)
			}
			continue
		}
		if err := field.Validate(value); err != nil {
			return nil, fmt.Errorf("invalid value for %s", read.Name)
		}
		values[read.Name] = value
	}
	return values, nil
}

func toolEnvironment(registry envregistry.Registry, managed bool) []string {
	var env []string
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "BMCSHELL_BRIDGE_") {
			continue
		}
		if managed {
			if _, registered := registry[key]; registered || key == "BMCSHELL_CONTEXT" {
				continue
			}
		}
		env = append(env, pair)
	}
	return env
}

func readStringMap(path string, limit int64) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if int64(len(data)) > limit || json.Unmarshal(data, &raw) != nil || raw == nil {
		return nil, errors.New("expected a JSON object with string values within the size limit")
	}
	values := make(map[string]string, len(raw))
	for key, entry := range raw {
		var value *string
		if json.Unmarshal(entry, &value) != nil || value == nil {
			return nil, errors.New("expected a JSON object with string values")
		}
		values[key] = *value
	}
	return values, nil
}

func applyToolWrites(path, destination string, t packages.Tool, registry envregistry.Registry, ctx session.Context) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	writes, err := readStringMap(path, 8192)
	if err != nil {
		return err
	}
	for name, value := range writes {
		declared := false
		for _, write := range t.EnvWrites {
			declared = declared || name == write.Name
		}
		field, registered := registry[name]
		if !declared || !registered || field.Sensitive || field.Validate(value) != nil {
			return fmt.Errorf("invalid environment write %q", name)
		}
		ctx[field.Key] = value
	}
	data, err := json.Marshal(ctx)
	if err != nil {
		return err
	}
	if len(data) > 8192 {
		return errors.New("updated session exceeds 8192 bytes")
	}
	return os.WriteFile(destination, data, 0600)
}
