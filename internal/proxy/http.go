package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

const EnvName = "BMCSHELL_PROXY_ADDRESS"

// HTTPTransport applies the explicit BMC proxy to every HTTP(S) request.
func HTTPTransport(address string) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if address != "" {
		transport.Proxy = http.ProxyURL(&url.URL{Scheme: "socks5h", Host: address})
	}
	return transport
}

func Environment(environ []string, address string) []string {
	result := make([]string, 0, len(environ)+1)
	for _, pair := range environ {
		name, _, _ := strings.Cut(pair, "=")
		name = strings.ToUpper(name)
		if strings.HasPrefix(name, "BMCSHELL_BRIDGE_") {
			continue
		}
		switch name {
		case EnvName, "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		}
		result = append(result, pair)
	}
	if address != "" {
		result = append(result, EnvName+"="+address)
	}
	return result
}
