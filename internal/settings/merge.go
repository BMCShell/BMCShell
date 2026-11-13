package settings

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// MergeDraft applies the draft's changes to the latest saved configuration.
// A change already present in current can be safely replayed after an
// interruption between saving the configuration and removing the checkpoint.
func MergeDraft(base, edited, current Config) (Config, error) {
	merged := current
	var err error
	merged.Theme, err = mergeValue("theme.active", base.Theme, edited.Theme, current.Theme)
	if err != nil {
		return Config{}, err
	}
	merged.ThemeOverride, err = mergeValue("theme.override", base.ThemeOverride, edited.ThemeOverride, current.ThemeOverride)
	if err != nil {
		return Config{}, err
	}
	resolver, err := mergeValue("use.default_resolver", optionalValue(base.DefaultResolver),
		optionalValue(edited.DefaultResolver), optionalValue(current.DefaultResolver))
	if err != nil {
		return Config{}, err
	}
	if resolver.present {
		merged.DefaultResolver = &resolver.value
	} else {
		merged.DefaultResolver = nil
	}
	merged.EnvDefaults, err = mergeMap("", base.EnvDefaults, edited.EnvDefaults, current.EnvDefaults)
	if err != nil {
		return Config{}, err
	}
	if err := mergePluginSettings(&merged, base, edited); err != nil {
		return Config{}, err
	}
	merged.Sources, err = mergeSources(base.Sources, edited.Sources, current.Sources)
	if err != nil {
		return Config{}, err
	}
	return merged, nil
}

type draftValue[T comparable] struct {
	value   T
	present bool
}

func optionalValue[T comparable](p *T) draftValue[T] {
	if p == nil {
		return draftValue[T]{}
	}
	return draftValue[T]{value: *p, present: true}
}

func mapValue[T comparable](values map[string]T, key string) draftValue[T] {
	value, present := values[key]
	return draftValue[T]{value: value, present: present}
}

func mergeValue[T comparable](key string, base, edited, current T) (T, error) {
	switch {
	case base == edited || current == edited:
		return current, nil
	case current == base:
		return edited, nil
	default:
		return current, draftConflict(key)
	}
}

func draftConflict(key string) error {
	return fmt.Errorf("设置 %q 已在其他终端修改", key)
}

func editedKeys[T any](values ...map[string]T) []string {
	keys := make(map[string]struct{})
	for _, fields := range values {
		for key := range fields {
			keys[key] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(keys))
}

func mergeMap[T comparable](prefix string, base, edited, current map[string]T) (map[string]T, error) {
	if maps.Equal(base, edited) {
		return current, nil
	}
	merged := make(map[string]T, len(current))
	maps.Copy(merged, current)
	for _, key := range editedKeys(base, edited) {
		value, err := mergeValue(prefix+key, mapValue(base, key), mapValue(edited, key), mapValue(current, key))
		if err != nil {
			return nil, err
		}
		if value.present {
			merged[key] = value.value
		} else {
			delete(merged, key)
		}
	}
	return merged, nil
}

type pluginOverride struct {
	plugin, legacy draftValue[string]
}

func pluginSetting(cfg Config, id, key string) pluginOverride {
	return pluginOverride{
		plugin: mapValue(cfg.PluginSettings[id], key),
		legacy: mapValue(cfg.ThemeSettings[id], key),
	}
}

func (v pluginOverride) effective() draftValue[string] {
	if v.plugin.present {
		return v.plugin
	}
	return v.legacy
}

func setPackageValue(settings *map[string]map[string]string, id, key string, value draftValue[string]) {
	if !value.present {
		delete((*settings)[id], key)
		if len((*settings)[id]) == 0 {
			delete(*settings, id)
		}
		return
	}
	if *settings == nil {
		*settings = make(map[string]map[string]string)
	}
	if (*settings)[id] == nil {
		(*settings)[id] = make(map[string]string)
	}
	(*settings)[id][key] = value.value
}

func mergePluginSettings(merged *Config, base, edited Config) error {
	merged.PluginSettings = clonePackageValues(merged.PluginSettings)
	merged.ThemeSettings = clonePackageValues(merged.ThemeSettings)
	for _, id := range editedKeys(base.PluginSettings, base.ThemeSettings, edited.PluginSettings, edited.ThemeSettings) {
		for _, key := range editedKeys(base.PluginSettings[id], base.ThemeSettings[id], edited.PluginSettings[id], edited.ThemeSettings[id]) {
			before, after := pluginSetting(base, id, key), pluginSetting(edited, id, key)
			if before == after {
				continue
			}
			current := pluginSetting(*merged, id, key).effective()
			// A CLI write can move a legacy theme setting into PluginSettings.
			// Compare the effective override so a concurrent move does not hide
			// a conflict on the same logical setting.
			if current != before.effective() && current != after.effective() {
				return draftConflict("plugin." + id + "." + key)
			}
			setPackageValue(&merged.PluginSettings, id, key, after.plugin)
			setPackageValue(&merged.ThemeSettings, id, key, after.legacy)
		}
	}
	return nil
}

func sourceMap(sources *[]Source) map[string]Source {
	if sources == nil {
		return nil
	}
	values := make(map[string]Source, len(*sources))
	for _, source := range *sources {
		values[source.Name] = source
	}
	return values
}

func mergeSources(base, edited, current *[]Source) (*[]Source, error) {
	before, after := sourceMap(base), sourceMap(edited)
	if maps.Equal(before, after) {
		return current, nil
	}
	values, err := mergeMap("plugin.source.", before, after, sourceMap(current))
	if err != nil {
		return nil, err
	}
	sources := make([]Source, 0, len(values))
	for _, source := range values {
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(a, b Source) int {
		if a.Official != b.Official {
			if a.Official {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return &sources, nil
}
