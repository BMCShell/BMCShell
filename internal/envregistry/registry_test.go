package envregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bmcshell/doc"
)

func TestFrameworkRegistry(t *testing.T) {
	registry, err := LoadFramework("")
	if err != nil {
		t.Fatal(err)
	}
	if ip := registry["BMCSHELL_TARGET_IP"]; ip.Key != "target.ip" || ip.Validate("2001:db8::2") != nil ||
		ip.Validate("wrong") == nil || ip.Validate("fe80::1%eth0") == nil {
		t.Fatal("invalid target IP definition")
	}
	if tls := registry["BMCSHELL_TLS_MODE"]; tls.FrameworkDefault == nil || *tls.FrameworkDefault != "verify" {
		t.Fatal("framework TLS default is missing")
	}
	if _, present := registry["SBMC_PLATFORM_CID"]; present {
		t.Fatal("framework must not register the SBMC sample field")
	}

	path := filepath.Join(t.TempDir(), FrameworkFilename)
	edited := strings.Replace(doc.RegistryCSV(), `"verify","拟新增"`, `"insecure","拟新增"`, 1)
	if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err = LoadFramework(path)
	if err != nil {
		t.Fatal(err)
	}
	if tls := registry["BMCSHELL_TLS_MODE"]; tls.FrameworkDefault == nil || *tls.FrameworkDefault != "insecure" {
		t.Fatal("edited framework TLS default was not loaded")
	}

	edited = strings.Replace(edited, `"BMCSHELL_AUTH_PASSWORD","敏感字段","需要认证的工具","按命令要求","是"`,
		`"BMCSHELL_AUTH_PASSWORD","敏感字段","需要认证的工具","按命令要求","否"`, 1)
	if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFramework(path); err == nil {
		t.Fatal("edited registry must not remove built-in credential sensitivity")
	}
}

func TestPluginRegistry(t *testing.T) {
	registry, err := LoadFramework("")
	if err != nil {
		t.Fatal(err)
	}
	const header = "\uFEFFname,key,sensitive,default\r\n"
	if err := registry.AddPlugin(strings.NewReader(header+
		"SBMC_PLATFORM_CID,oem.sbmc.cid,false,\r\n"+
		"SBMC_PLATFORM_PROJECT_NAME,oem.sbmc.project_name,false,\"例子, 3.0\"\r\n"), "sbmc"); err != nil {
		t.Fatal(err)
	}
	field := registry["SBMC_PLATFORM_PROJECT_NAME"]
	if field.PluginDefault == nil || *field.PluginDefault != "例子, 3.0" || field.FrameworkDefault != nil {
		t.Fatal("plugin CSV default was not loaded")
	}
	for _, row := range []string{
		"SBMC_PLATFORM_CID,oem.sbmc.new_key,false,",
		"SBMC_PLATFORM_DUPLICATE,oem.sbmc.cid,false,",
		"BMCSHELL_TLS_MODE,,false,insecure",
		"SBMC_AUTH_PASSWORD,,true,example",
		"SBMC_PLATFORM_OTHER,oem.other.field,false,",
	} {
		data := header + "SBMC_PLATFORM_UNIQUE,,false,\n" + row + "\n"
		if err := registry.AddPlugin(strings.NewReader(data), "sbmc"); err == nil {
			t.Errorf("unexpected success for %s", row)
		}
		if _, exists := registry["SBMC_PLATFORM_UNIQUE"]; exists {
			t.Fatal("invalid plugin CSV partially modified the registry")
		}
	}
}
