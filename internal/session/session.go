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

	"bmcshell/internal/packages"
	"bmcshell/internal/settings"
)

type Context map[string]string

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

func Resolve(resolver Resolver, ip string, argv []string, in io.Reader, terminal io.Writer) (Context, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.Zone() != "" {
		return nil, fmt.Errorf("invalid IP address %q", ip)
	}
	ctx := Context{"target.ip": ip, "target.resolver": resolver.Name}
	if resolver.Name == "generic" {
		if len(argv) != 0 {
			return nil, errors.New("generic resolver takes no extra arguments")
		}
		return ctx, nil
	}
	path, err := resolver.Package.File(resolver.Package.Provides.Resolvers[resolver.Name].Entry)
	if err != nil {
		return nil, err
	}
	result, err := os.CreateTemp("", "bmcshell-resolve-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(result.Name())
	if err := result.Close(); err != nil {
		return nil, err
	}
	cmd := exec.Command(path, append([]string{ip}, argv...)...)
	cmd.Env = append(os.Environ(), "BMCSHELL_ENTRY="+resolver.Name, "BMCSHELL_RESULT_FILE="+result.Name(), "BMCSHELL_CONTEXT=")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, terminal, terminal
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("resolver %q: %w", resolver.Name, err)
	}
	f, err := os.Open(result.Name())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return nil, err
	}
	var candidate Context
	if len(data) > 8192 || json.Unmarshal(data, &candidate) != nil || candidate == nil {
		return nil, errors.New("resolver must return a JSON object of session fields")
	}
	for key, value := range candidate {
		switch {
		case key == "target.ip":
			if value != ip {
				return nil, errors.New("resolver changed the requested IP")
			}
		case key == "target.model", strings.HasPrefix(key, "oem."+resolver.Name+"."):
			if !Printable(key) || !Printable(value) {
				return nil, fmt.Errorf("invalid value for session field %q", key)
			}
			ctx[key] = value
		default:
			return nil, fmt.Errorf("unsupported session field %q", key)
		}
	}
	return ctx, nil
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
		if !Printable(key) || !Printable(value) {
			return nil, errors.New("invalid text in shell context")
		}
	}
	return ctx, nil
}

func Printable(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
