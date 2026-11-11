// Package proxy relays the native BMC tools through a local, no-auth SOCKS5 proxy.
package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

const setupTimeout = 5 * time.Second

const (
	connectCommand   = 1
	associateCommand = 3
	defaultIPMIPort  = 623
)

var loopback = netip.AddrFrom4([4]byte{127, 0, 0, 1})

func proxyAddress(value string) (netip.AddrPort, error) {
	address, err := netip.ParseAddrPort(value)
	if err != nil || address.Addr() != loopback || address.Port() == 0 {
		return netip.AddrPort{}, errors.New("SOCKS5 proxy must be 127.0.0.1:<port>")
	}
	return address, nil
}

func endpoint(ip string, port uint16) (netip.AddrPort, error) {
	address, err := netip.ParseAddr(ip)
	if err != nil || !address.Is4() || port == 0 {
		return netip.AddrPort{}, errors.New("SOCKS5 target must be an IPv4 address with a valid port")
	}
	return netip.AddrPortFrom(address, port), nil
}

func ipmiEndpoint(host string, port uint16, family string) (netip.AddrPort, error) {
	network := "ip"
	switch family {
	case "":
	case "4", "6":
		network += family
	default:
		return netip.AddrPort{}, errors.New("IPMI address family must be 4 or 6")
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
		defer cancel()
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, network, host)
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("resolve IPMI target %q: %w", host, err)
		}
		if len(addresses) == 0 {
			return netip.AddrPort{}, fmt.Errorf("no address for IPMI target %q", host)
		}
		address = addresses[0]
	}
	if family != "6" {
		address = address.Unmap()
	}
	if address.Zone() != "" || (family == "4" && !address.Is4()) || (family == "6" && !address.Is6()) {
		return netip.AddrPort{}, fmt.Errorf("IPMI target %q cannot be used with the requested address family over SOCKS5", host)
	}
	return netip.AddrPortFrom(address, port), nil
}

func wireAddress(address netip.AddrPort) []byte {
	port := address.Port()
	if address.Addr().Is6() {
		ip := address.Addr().As16()
		wire := append([]byte{4}, ip[:]...)
		return append(wire, byte(port>>8), byte(port))
	}
	ip := address.Addr().As4()
	return []byte{1, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
}

type reply struct {
	host string
	port uint16
}

func selectNoAuth(conn io.ReadWriter) error {
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		return err
	}
	if method != [2]byte{5, 0} {
		return errors.New("SOCKS5 server must accept the no-auth method")
	}
	return nil
}

func negotiate(conn io.ReadWriter, command byte, destination netip.AddrPort) (reply, error) {
	if err := selectNoAuth(conn); err != nil {
		return reply{}, err
	}
	if _, err := conn.Write(append([]byte{5, command, 0}, wireAddress(destination)...)); err != nil {
		return reply{}, err
	}
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return reply{}, err
	}
	if header[0] != 5 || header[2] != 0 {
		return reply{}, errors.New("invalid SOCKS5 reply header")
	}
	if header[1] != 0 {
		op := "CONNECT"
		if command == associateCommand {
			op = "UDP ASSOCIATE"
		}
		return reply{}, fmt.Errorf("SOCKS5 %s failed (reply %d)", op, header[1])
	}
	return readReplyAddress(conn, header[3])
}

func readReplyAddress(conn io.Reader, kind byte) (reply, error) {
	var size int
	switch kind {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return reply{}, err
		}
		size = int(length[0])
		if size == 0 {
			return reply{}, errors.New("empty SOCKS5 reply hostname")
		}
	default:
		return reply{}, errors.New("invalid SOCKS5 reply address type")
	}
	address := make([]byte, size+2)
	if _, err := io.ReadFull(conn, address); err != nil {
		return reply{}, err
	}
	host := string(address[:size])
	if kind != 3 {
		ip, _ := netip.AddrFromSlice(address[:size])
		host = ip.String()
	}
	return reply{host, binary.BigEndian.Uint16(address[size:])}, nil
}

func openProxy(address netip.AddrPort, command byte, target netip.AddrPort) (*net.TCPConn, reply, error) {
	socket, err := net.DialTimeout("tcp4", address.String(), setupTimeout)
	if err != nil {
		return nil, reply{}, fmt.Errorf("connect to SOCKS5 proxy %s: %w", address, err)
	}
	control := socket.(*net.TCPConn)
	if err := control.SetDeadline(time.Now().Add(setupTimeout)); err != nil {
		control.Close()
		return nil, reply{}, err
	}
	res, err := negotiate(control, command, target)
	if err == nil {
		err = control.SetDeadline(time.Time{})
	}
	if err != nil {
		control.Close()
		return nil, reply{}, err
	}
	return control, res, nil
}

// CheckCapabilities checks the selected SOCKS5 listener before launching a
// plugin that declares a network requirement. A TCP destination is checked
// by the actual request, while UDP requires an available association.
func CheckCapabilities(address string, capabilities []string) error {
	if address == "" || len(capabilities) == 0 {
		return nil
	}
	addr, err := proxyAddress(address)
	if err != nil {
		return err
	}
	if slices.Contains(capabilities, "udp") {
		udp, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(loopback, 0)))
		if err != nil {
			return err
		}
		defer udp.Close()
		control, res, err := openProxy(addr, associateCommand, udp.LocalAddr().(*net.UDPAddr).AddrPort())
		if err != nil {
			return err
		}
		defer control.Close()
		_, err = loopbackRelay(res, addr)
		return err
	}
	conn, err := net.DialTimeout("tcp4", addr.String(), setupTimeout)
	if err != nil {
		return fmt.Errorf("connect to SOCKS5 proxy %s: %w", addr, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(setupTimeout)); err != nil {
		return err
	}
	return selectNoAuth(conn)
}

func loopbackRelay(res reply, peer netip.AddrPort) (netip.AddrPort, error) {
	invalid := errors.New("SOCKS5 UDP relay must be reachable on local IPv4 loopback")
	if res.port == 0 {
		return netip.AddrPort{}, invalid
	}
	ip, err := netip.ParseAddr(res.host)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", res.host)
		if err != nil || len(addrs) == 0 {
			return netip.AddrPort{}, invalid
		}
		ip = addrs[0]
	}
	ip = ip.Unmap()
	if ip == netip.IPv4Unspecified() {
		ip = peer.Addr()
	}
	if !ip.Is4() || !ip.IsLoopback() {
		return netip.AddrPort{}, invalid
	}
	return netip.AddrPortFrom(ip, res.port), nil
}

func parsePort(value string) (uint16, error) {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, errors.New("expected a port from 1 to 65535")
	}
	return uint16(port), nil
}

func parseIPMIPort(value string) (uint16, error) {
	if value == "" {
		return defaultIPMIPort, nil
	}
	// ipmitool uses strtol with base 0 and treats zero as the default port.
	value = strings.TrimLeft(value, " \t\n\v\f\r")
	digits := strings.TrimLeft(value, "+-")
	base := 10
	switch {
	case strings.HasPrefix(digits, "0x") || strings.HasPrefix(digits, "0X"):
		base = 0
	case strings.HasPrefix(digits, "0"):
		base = 8
	}
	port, err := strconv.ParseInt(value, base, 32)
	if err != nil || port < 0 || port > 65535 || strings.Contains(value, "_") {
		return 0, errors.New("expected an IPMI port from 0 to 65535")
	}
	if port == 0 {
		return defaultIPMIPort, nil
	}
	return uint16(port), nil
}
