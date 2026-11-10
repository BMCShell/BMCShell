package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Resolver struct {
	ExtensionID string `json:"extensionId"`
	Export      string `json:"export"`
}

type Config struct {
	LibrarySources  map[string]string `json:"library.sources,omitempty"`
	EnvDefaults     map[string]string `json:"env.defaults,omitempty"`
	DefaultResolver *Resolver         `json:"use.default_resolver,omitempty"`
	Theme           string            `json:"theme.active,omitempty"`
	Proxy           string            `json:"proxy.address,omitempty"`
}

func Load(root string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("settings.json: %w", err)
	}
	if ref := cfg.DefaultResolver; ref != nil && (ref.ExtensionID == "" || ref.Export == "") {
		return cfg, errors.New("settings.json: default resolver needs an extension ID and export")
	}
	if cfg.Proxy != "" {
		if address, err := ProxyAddress(cfg.Proxy); err != nil || address != cfg.Proxy {
			return cfg, errors.New("settings.json: proxy.address must be a canonical 127.0.0.1:port")
		}
	}
	return cfg, nil
}

func ProxyAddress(value string) (string, error) {
	switch value {
	case "off":
		return "", nil
	case "on":
		return "127.0.0.1:1080", nil
	}
	host, rawPort, err := net.SplitHostPort(value)
	if err == nil && host == "127.0.0.1" && rawPort != "" && len(rawPort) <= 5 &&
		strings.IndexFunc(rawPort, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		port, err := strconv.ParseUint(rawPort, 10, 16)
		if err == nil && port > 0 {
			return fmt.Sprintf("127.0.0.1:%d", port), nil
		}
	}
	return "", errors.New("proxy must be on, off, or 127.0.0.1:<port 1-65535>")
}

func Save(root string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(root, "settings.json"))
}
