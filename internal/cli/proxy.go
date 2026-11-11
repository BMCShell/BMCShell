package cli

import (
	"errors"
	"fmt"
	"os"

	"bmcshell/internal/proxy"
	"bmcshell/internal/settings"
)

const proxyUsage = "usage: bmc proxy [on|off|-m 127.0.0.1:port]"

type proxyConfig struct {
	active    string
	preferred string
}

func (a *app) configureProxy(args []string) (proxyConfig, error) {
	cfg, err := settings.Load(a.home)
	if err != nil {
		return proxyConfig{}, err
	}
	active, err := shellProxyAddress(cfg.Proxy)
	if err != nil {
		return proxyConfig{}, err
	}
	preferred := cfg.ProxyPreferred
	if preferred == "" {
		preferred = cfg.Proxy
	}
	if preferred == "" {
		preferred = "127.0.0.1:1080"
	}

	switch {
	case len(args) == 0:
		return proxyConfig{active, preferred}, nil
	case len(args) == 1 && args[0] == "on":
		active = preferred
	case len(args) == 1 && args[0] == "off":
		active = ""
	case len(args) == 2 && args[0] == "-m":
		preferred, err = settings.ProxyAddress(args[1])
		if err != nil || args[1] == "on" || preferred == "" {
			return proxyConfig{}, errors.New(proxyUsage)
		}
		if active != "" {
			active = preferred
		}
	default:
		return proxyConfig{}, errors.New(proxyUsage)
	}

	cfg.Proxy, cfg.ProxyPreferred = active, preferred
	if err := settings.Save(a.home, cfg); err != nil {
		return proxyConfig{}, err
	}
	return proxyConfig{active, preferred}, nil
}

func (a *app) proxyCommand(args []string) error {
	cfg, err := a.configureProxy(args)
	if err != nil {
		return err
	}
	if cfg.active == "" {
		fmt.Fprintf(a.out, "BMC 全局代理：关闭（已配置 %s）\n", cfg.preferred)
	} else {
		fmt.Fprintf(a.out, "BMC SOCKS5：%s\n", cfg.active)
	}
	return nil
}

func shellProxyAddress(fallback string) (string, error) {
	if value, ok := os.LookupEnv("BMCSHELL_BRIDGE_PROXY"); ok {
		return canonicalProxyAddress(value)
	}
	return fallback, nil
}

func canonicalProxyAddress(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	address, err := settings.ProxyAddress(value)
	if err != nil || address != value {
		return "", errors.New("invalid BMCShell SOCKS5 proxy address")
	}
	return address, nil
}

func (a *app) proxyAddress() (string, error) {
	if value, ok := os.LookupEnv("BMCSHELL_BRIDGE_PROXY"); ok {
		return canonicalProxyAddress(value)
	}
	cfg, err := settings.Load(a.home)
	return cfg.Proxy, err
}

// proxyConfigBridge returns settings and any requested change in one call so
// the shell can apply the same value without reading another process's update.
func (a *app) proxyConfigBridge(args []string) error {
	cfg, err := a.configureProxy(args)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s|%s\n", cfg.active, cfg.preferred)
	return nil
}

func (a *app) proxyAddressBridge(args []string) error {
	// Older Bash tool packages still pass the protocol name.
	if len(args) > 1 || (len(args) == 1 && args[0] != "ipmi") {
		return errors.New("invalid proxy address request")
	}
	address, err := a.proxyAddress()
	if err != nil {
		return err
	}
	fmt.Fprint(a.out, address)
	return nil
}

func (a *app) proxyRunBridge(args []string) error {
	if len(args) == 0 {
		return errors.New("missing proxy protocol (ssh, ipmi, or snmp)")
	}
	address, err := a.proxyAddress()
	// OpenSSH and standalone plugins can launch the relay directly.
	if _, fromBashBridge := os.LookupEnv("BMCSHELL_BRIDGE_PROXY"); !fromBashBridge {
		if addressFromCaller, ok := os.LookupEnv(proxy.EnvName); ok {
			address, err = canonicalProxyAddress(addressFromCaller)
		}
	}
	if err != nil {
		return err
	}
	if address == "" {
		return errors.New("SOCKS5 proxy is disabled")
	}
	return proxy.Run(address, args[0], args[1:], a.in, a.out, a.errOut)
}
