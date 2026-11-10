package packages

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"bmcshell/internal/envregistry"
)

// Environment loads a complete registry and checks tool contracts before returning it.
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
			return nil, fmt.Errorf("package %s environment registry: %w", p.ID, err)
		}
		for name := range registry {
			if _, ok := owners[name]; !ok {
				owners[name] = p.ID
			}
		}
	}
	for _, p := range list {
		for tool, spec := range p.Provides.Tools {
			if err := checkToolEnvironment(registry, owners, p, spec); err != nil {
				return nil, fmt.Errorf("tool %s: %w", tool, err)
			}
		}
	}
	return registry, nil
}

func checkToolEnvironment(registry envregistry.Registry, owners map[string]string, p Package, spec Tool) error {
	reads := make(map[string]bool, len(spec.EnvReads))
	for _, read := range spec.EnvReads {
		if _, exists := registry[read.Name]; !exists || reads[read.Name] {
			return fmt.Errorf("unknown or duplicate environment dependency %q", read.Name)
		}
		reads[read.Name] = true
	}
	writes := make(map[string]bool, len(spec.EnvWrites))
	for _, write := range spec.EnvWrites {
		field, exists := registry[write.Name]
		if !exists || owners[write.Name] != p.ID || field.Sensitive || writes[write.Name] {
			return fmt.Errorf("unauthorized or duplicate environment write %q", write.Name)
		}
		writes[write.Name] = true
	}
	return nil
}
