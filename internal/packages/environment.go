package packages

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"bmcshell/internal/envregistry"
)

// Environment loads the complete registry and checks tool and resolver contracts.
func (s *Store) Environment(list []Package) (envregistry.Registry, error) {
	path := filepath.Join(filepath.Dir(s.dir), envregistry.FrameworkFilename)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		path = ""
		if executable, err := os.Executable(); err == nil {
			shipped := filepath.Join(filepath.Dir(executable), "doc", envregistry.FrameworkFilename)
			if _, err := os.Stat(shipped); !errors.Is(err, os.ErrNotExist) {
				path = shipped
			}
		}
	}
	registry, err := envregistry.LoadFramework(path)
	if err != nil {
		return nil, err
	}
	owners := make(map[string]string)
	for name := range registry {
		owners[name] = ""
	}
	for _, p := range list {
		if p.EnvRegistry == "" {
			continue
		}
		path, err := p.File(p.EnvRegistry)
		if err != nil {
			return nil, err
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		err = registry.AddPlugin(file, p.Publisher)
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("包 %s 的环境变量登记表：%w", p.ID, err)
		}
		for name := range registry {
			if _, ok := owners[name]; !ok {
				owners[name] = p.ID
			}
		}
	}
	settingOwners := make(map[string]string)
	for _, p := range list {
		for _, field := range p.Settings {
			if field.Env == "" {
				continue
			}
			if _, exists := registry[field.Env]; exists {
				return nil, fmt.Errorf("包 %s 的设置环境变量 %q 已在环境变量登记表声明", p.ID, field.Env)
			}
			if owner, exists := settingOwners[field.Env]; exists {
				return nil, fmt.Errorf("设置环境变量 %q 在包 %s 和包 %s 中重复声明", field.Env, owner, p.ID)
			}
			settingOwners[field.Env] = p.ID
		}
		for tool, spec := range p.Provides.Tools {
			if err := checkToolEnvironment(registry, owners, p, spec); err != nil {
				return nil, fmt.Errorf("工具 %s：%w", tool, err)
			}
		}
		for resolver, spec := range p.Provides.Resolvers {
			if err := checkEnvironmentReads(registry, spec.EnvReads, false); err != nil {
				return nil, fmt.Errorf("解析器 %s：%w", resolver, err)
			}
			for _, status := range spec.StatusFields {
				field, ok := registry.ByKey(status.Key)
				owner := owners[field.Name]
				if !ok || field.Sensitive || (owner != "" && owner != p.ID) {
					return nil, fmt.Errorf("解析器 %s 的状态字段 %q 未授权或未登记", resolver, status.Key)
				}
			}
		}
	}
	return registry, nil
}

func checkToolEnvironment(registry envregistry.Registry, owners map[string]string, p Package, spec Tool) error {
	if err := checkEnvironmentReads(registry, spec.EnvReads, true); err != nil {
		return err
	}
	writes := make(map[string]bool, len(spec.EnvWrites))
	for _, write := range spec.EnvWrites {
		field, exists := registry[write.Name]
		if !exists || owners[write.Name] != p.ID || field.Sensitive || writes[write.Name] {
			return fmt.Errorf("环境变量写入项 %q 未授权或重复", write.Name)
		}
		writes[write.Name] = true
	}
	return nil
}

func checkEnvironmentReads(registry envregistry.Registry, reads []envregistry.Read, allowSensitive bool) error {
	seen := make(map[string]bool, len(reads))
	for _, read := range reads {
		field, exists := registry[read.Name]
		if !exists || seen[read.Name] {
			return fmt.Errorf("环境变量依赖 %q 未登记或重复", read.Name)
		}
		if field.Sensitive && !allowSensitive {
			return fmt.Errorf("环境变量依赖 %q 是敏感字段", read.Name)
		}
		seen[read.Name] = true
	}
	return nil
}
