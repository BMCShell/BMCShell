package cli

import (
	"errors"
	"fmt"
	"os"

	"bmcshell/internal/proxy"
	"bmcshell/internal/settings"
)

func (a *app) proxyCommand(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: bmc proxy [on|off|127.0.0.1:port]")
	}
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		cfg.Proxy, err = settings.ProxyAddress(args[0])
		if err != nil {
			return err
		}
		if err := settings.Save(a.home, cfg); err != nil {
			return err
		}
	}
	if cfg.Proxy == "" {
		fmt.Fprintln(a.out, "BMC 全局代理：关闭")
	} else {
		fmt.Fprintf(a.out, "BMC SOCKS5：%s\n", cfg.Proxy)
	}
	return nil
}

func (a *app) proxyAddress(ipmi bool) (string, error) {
	cfg, err := settings.Load(a.home)
	if err != nil {
		return "", err
	}
	if cfg.Proxy != "" || !ipmi {
		return cfg.Proxy, nil
	}
	if local := os.Getenv("BMCSHELL_IPMI_PROXY"); local != "" {
		return settings.ProxyAddress(local)
	}
	return "", nil
}

func (a *app) proxyAddressBridge(args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "ipmi") {
		return errors.New("usage: bridge proxy-address [ipmi]")
	}
	address, err := a.proxyAddress(len(args) == 1)
	if err != nil {
		return err
	}
	fmt.Fprint(a.out, address)
	return nil
}

func (a *app) proxyRunBridge(args []string) error {
	if len(args) == 0 {
		return errors.New("bridge proxy-run expects ssh, ipmi, or snmp")
	}
	address, err := a.proxyAddress(args[0] == "ipmi")
	if err != nil {
		return err
	}
	if address == "" {
		return errors.New("SOCKS5 proxy is disabled")
	}
	return proxy.Run(address, args[0], args[1:], a.in, a.out, a.errOut)
}
