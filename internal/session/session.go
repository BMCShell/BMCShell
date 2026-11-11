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

func SelectResolver(list []packages.Package, name string, fallback *settings.Resolver) (Resolver, error) {
	if (name == "" && fallback == nil) || name == "generic" {
		return Resolver{Name: "generic"}, nil
	}
	var providerID string
	if name == "" {
		providerID, name = fallback.ExtensionID, fallback.Export
	}
	var matches []Resolver
	for _, p := range list {
		if providerID != "" && p.ID != providerID {
			continue
		}
		if _, ok := p.Provides.Resolvers[name]; ok {
			matches = append(matches, Resolver{Name: name, Package: p})
		}
	}
	if len(matches) != 1 {
		return Resolver{}, fmt.Errorf("resolver %q must have one installed provider (found %d)", name, len(matches))
	}
	return matches[0], nil
}

func Resolve(resolver Resolver, ip string, argv []string, address string, environment []string, in io.Reader, terminal io.Writer) (Resolution, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.Zone() != "" {
		return Resolution{}, fmt.Errorf("invalid IP address %q", ip)
	}
	resolution := Resolution{Context: Context{"target.ip": ip, "target.resolver": resolver.Name}}
	if resolver.Name == "generic" {
		if len(argv) != 0 {
			return Resolution{}, errors.New("generic resolver takes no extra arguments")
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
		return Resolution{}, fmt.Errorf("resolver %q: %w", resolver.Name, err)
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
		return Resolution{}, errors.New("resolver must return a JSON object of session fields")
	}
	for key, value := range candidate {
		switch {
		case key == "target.ip":
			if value != ip {
				return Resolution{}, errors.New("resolver changed the requested IP")
			}
		case key == "snmp.enterprises":
			if err := (envregistry.Field{Name: "BMCSHELL_SNMP_ENTERPRISES"}).Validate(value); err != nil {
				return Resolution{}, err
			}
			resolution.Context[key] = value
		case key == "target.model", strings.HasPrefix(key, "oem."+resolver.Name+"."):
			if !Printable(key) || !Printable(value) {
				return Resolution{}, fmt.Errorf("invalid value for session field %q", key)
			}
			resolution.Context[key] = value
		case key == "auth.username" || key == "auth.password":
			if value == "" || !Printable(value) {
				return Resolution{}, errors.New("resolver returned invalid credentials")
			}
			if key == "auth.username" {
				resolution.Username = value
			} else {
				resolution.Password = value
			}
		default:
			return Resolution{}, fmt.Errorf("unsupported session field %q", key)
		}
	}
	if (resolution.Username == "") != (resolution.Password == "") {
		return Resolution{}, errors.New("resolver must return auth.username and auth.password together")
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
		return nil, fmt.Errorf("invalid shell context: %w", err)
	}
	addr, err := netip.ParseAddr(ctx["target.ip"])
	if err != nil || addr.Zone() != "" {
		return nil, errors.New("invalid IP in shell context")
	}
	for key, value := range ctx {
		if key == "snmp.enterprises" {
			if err := (envregistry.Field{Name: "BMCSHELL_SNMP_ENTERPRISES"}).Validate(value); err != nil {
				return nil, err
			}
		}
		if !Printable(key) || !Printable(value) {
			return nil, errors.New("invalid text in shell context")
		}
	}
	return ctx, nil
}

func Printable(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
