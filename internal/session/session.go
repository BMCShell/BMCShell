package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"

	"bmcshell/internal/envregistry"
	"bmcshell/internal/packages"
	"bmcshell/internal/proxy"
	"bmcshell/internal/settings"
)

type Context map[string]string

type Resolution struct {
	Context  Context
	Username string
	Password string
}

type Resolver struct {
	Name    string
	Package packages.Package
}

// Selector is the unversioned public name of the resolver. Packages without
// a published name or with multiple resolvers use their export names.
func (r Resolver) Selector() string {
	if r.Package.ShortName != "" && len(r.Package.Provides.Resolvers) == 1 {
		return r.Package.Name
	}
	return r.Name
}

func SelectResolver(list []packages.Package, name string, fallback *settings.Resolver) (Resolver, error) {
	if (name == "" && fallback == nil) || name == "generic" {
		return Resolver{Name: "generic"}, nil
	}
	if strings.Contains(name, "@") {
		return Resolver{}, fmt.Errorf("请在指定解析器 %q 时省略版本", name)
	}
	var providerID string
	if name == "" {
		providerID, name = fallback.ExtensionID, fallback.Export
	}
	var providers []packages.Package
	var matches []Resolver
	for _, p := range list {
		if providerID != "" && p.ID != providerID {
			continue
		}
		if providerID == "" && p.ShortName != "" && p.Name == name && len(p.Provides.Resolvers) != 0 {
			providers = append(providers, p)
		}
		if _, ok := p.Provides.Resolvers[name]; ok {
			matches = append(matches, Resolver{Name: name, Package: p})
		}
	}
	if len(providers) > 1 {
		return Resolver{}, fmt.Errorf("解析器包名 %q 匹配了 %d 个已安装的包", name, len(providers))
	}
	if len(providers) == 1 {
		p := providers[0]
		if len(p.Provides.Resolvers) > 1 {
			return Resolver{}, fmt.Errorf("包 %q 提供多个解析器，请指定解析器的导出名称", name)
		}
		for export := range p.Provides.Resolvers {
			return Resolver{Name: export, Package: p}, nil
		}
	}
	if len(matches) != 1 {
		return Resolver{}, fmt.Errorf("解析器 %q 须由一个已安装的包提供，实际找到 %d 个", name, len(matches))
	}
	return matches[0], nil
}

func Resolve(resolver Resolver, ip string, argv []string, address string, environment []string, in io.Reader, terminal io.Writer) (Resolution, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.Zone() != "" {
		return Resolution{}, fmt.Errorf("IP 地址 %q 无效", ip)
	}
	resolution := Resolution{Context: Context{"target.ip": ip, "target.resolver": resolver.Selector()}}
	if resolver.Name == "generic" {
		if len(argv) != 0 {
			return Resolution{}, errors.New("generic 解析器不接收额外参数")
		}
		return resolution, nil
	}
	spec := resolver.Package.Provides.Resolvers[resolver.Name]
	if err := proxy.CheckCapabilities(address, spec.ProxyCapabilities); err != nil {
		return Resolution{}, err
	}
	path, err := resolver.Package.File(spec.Entry)
	if err != nil {
		return Resolution{}, err
	}
	result, err := os.CreateTemp("", "bmcshell-resolve-*.json")
	if err != nil {
		return Resolution{}, err
	}
	defer os.Remove(result.Name())
	if err := result.Close(); err != nil {
		return Resolution{}, err
	}
	cmd := exec.Command(path, append([]string{ip}, argv...)...)
	cmd.Env = append(proxy.Environment(environment, address), "BMCSHELL_ENTRY="+resolver.Name, "BMCSHELL_RESULT_FILE="+result.Name(), "BMCSHELL_CONTEXT=")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, terminal, terminal
	if err := cmd.Run(); err != nil {
		return Resolution{}, fmt.Errorf("解析器 %q：%w", resolver.Selector(), err)
	}
	f, err := os.Open(result.Name())
	if err != nil {
		return Resolution{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return Resolution{}, err
	}
	var candidate Context
	if len(data) > 8192 || json.Unmarshal(data, &candidate) != nil || candidate == nil {
		return Resolution{}, errors.New("解析器须将会话字段作为 JSON 对象返回")
	}
	for key, value := range candidate {
		switch {
		case key == "target.ip":
			if value != ip {
				return Resolution{}, errors.New("解析器返回的 IP 地址与请求不一致")
			}
		case key == "snmp.enterprises":
			if err := (envregistry.Field{Name: "BMCSHELL_SNMP_ENTERPRISES"}).Validate(value); err != nil {
				return Resolution{}, err
			}
			resolution.Context[key] = value
		case key == "target.model", strings.HasPrefix(key, "oem."+resolver.Name+"."):
			if !Printable(key) || !Printable(value) {
				return Resolution{}, fmt.Errorf("会话字段 %q 的值无效", key)
			}
			resolution.Context[key] = value
		case key == "auth.username" || key == "auth.password":
			if value == "" || !Printable(value) {
				return Resolution{}, errors.New("解析器返回了无效的登录凭据")
			}
			if key == "auth.username" {
				resolution.Username = value
			} else {
				resolution.Password = value
			}
		default:
			return Resolution{}, fmt.Errorf("不支持会话字段 %q", key)
		}
	}
	if (resolution.Username == "") != (resolution.Password == "") {
		return Resolution{}, errors.New("解析器须同时返回 auth.username 和 auth.password")
	}
	// This resolver returns credentials only after successful login.
	if resolver.Package.ID == "bf50a343-ad6c-4f8d-b4b7-43bb4475bc78" && resolution.Password != "" {
		resolution.Context["target.auth_verified"] = "true"
	}
	return resolution, nil
}

func Current() (Context, error) {
	raw := os.Getenv("BMCSHELL_CONTEXT")
	if raw == "" {
		return nil, nil
	}
	var ctx Context
	if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
		return nil, fmt.Errorf("Bash 会话数据无效：%w", err)
	}
	addr, err := netip.ParseAddr(ctx["target.ip"])
	if err != nil || addr.Zone() != "" {
		return nil, errors.New("Bash 会话中的 IP 地址无效")
	}
	for key, value := range ctx {
		if key == "snmp.enterprises" {
			if err := (envregistry.Field{Name: "BMCSHELL_SNMP_ENTERPRISES"}).Validate(value); err != nil {
				return nil, err
			}
		}
		if !Printable(key) || !Printable(value) {
			return nil, errors.New("Bash 会话中含有无效文本")
		}
	}
	return ctx, nil
}

func Printable(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
