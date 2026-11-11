package cli

import (
	"testing"

	"bmcshell/internal/settings"
)

func TestProxyAddressChangePreservesBashSwitch(t *testing.T) {
	a := app{home: t.TempDir()}
	t.Setenv("BMCSHELL_BRIDGE_PROXY", "")
	cfg, err := a.configureProxy([]string{"-m", "127.0.0.1:1081"})
	if err != nil || cfg.active != "" || cfg.preferred != "127.0.0.1:1081" {
		t.Fatalf("changing the disabled proxy returned %+v, %v", cfg, err)
	}
	cfg, err = a.configureProxy([]string{"on"})
	if err != nil || cfg.active != "127.0.0.1:1081" {
		t.Fatalf("enabling the preferred proxy returned %+v, %v", cfg, err)
	}
	if addr, err := a.proxyAddress(); err != nil || addr != "" {
		t.Fatalf("an already loaded Bash that is off should retain its state; got %q, %v", addr, err)
	}
	t.Setenv("BMCSHELL_BRIDGE_PROXY", cfg.active)
	cfg, err = a.configureProxy([]string{"-m", "127.0.0.1:1082"})
	if err != nil || cfg.active != "127.0.0.1:1082" {
		t.Fatalf("changing the enabled proxy returned %+v, %v", cfg, err)
	}
	if _, err := a.configureProxy([]string{"off"}); err != nil {
		t.Fatal(err)
	}
	saved, err := settings.Load(a.home)
	if err != nil || saved.Proxy != "" || saved.ProxyPreferred != "127.0.0.1:1082" {
		t.Fatalf("disabling the proxy persisted %+v, %v", saved, err)
	}
}
