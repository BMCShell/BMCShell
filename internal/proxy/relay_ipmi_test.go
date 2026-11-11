package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

const ipmiChildEnv = "BMCSHELL_PROXY_TEST_IPMI_NATIVE"

var ipmiChildArgs = []string{"-6", "-I", "lanplus", "-U", "test-user", "-P", "test password", "raw", "6", "1"}

func TestMain(m *testing.M) {
	if os.Getenv(ipmiChildEnv) != "1" {
		os.Exit(m.Run())
	}
	if err := runIPMIChild(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(17)
}

func runIPMIChild(args []string) error {
	if len(args) < 4 || !slices.Equal(args[:3], []string{"-H", "::1", "-p"}) || !slices.Equal(args[4:], ipmiChildArgs) {
		return fmt.Errorf("unexpected native arguments: %q", args)
	}
	socket, err := net.DialTimeout("udp6", net.JoinHostPort(args[1], args[3]), setupTimeout)
	if err != nil {
		return err
	}
	defer socket.Close()
	if err := socket.SetDeadline(time.Now().Add(setupTimeout)); err != nil {
		return err
	}
	if _, err := socket.Write([]byte("request")); err != nil {
		return err
	}
	var reply [32]byte
	n, err := socket.Read(reply[:])
	if err != nil || string(reply[:n]) != "reply" {
		return fmt.Errorf("native UDP reply = %q, %v", reply[:n], err)
	}
	_, err = os.Stdout.Write([]byte{0, 0xff})
	return err
}

func TestIPMINativeRelaysIPv6(t *testing.T) {
	server := localServer(t)
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		skipBlockedLoopback(t, err)
		t.Fatal(err)
	}
	defer udp.Close()
	if err := udp.SetDeadline(time.Now().Add(setupTimeout)); err != nil {
		t.Fatal(err)
	}
	host := netip.MustParseAddr("2001:db8::99")
	ip := host.As16()
	header := append([]byte{0, 0, 0, 4}, ip[:]...)
	header = append(header, 0x18, 0x61) // 6241
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- func() error {
			control, err := server.AcceptTCP()
			if err != nil {
				return err
			}
			defer control.Close()
			if _, err := receiveRequest(control, associateCommand, udp.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
				return err
			}
			var packet [64]byte
			n, from, err := udp.ReadFromUDPAddrPort(packet[:])
			if err != nil {
				return err
			}
			if !bytes.HasPrefix(packet[:n], header) || string(packet[len(header):n]) != "request" {
				return fmt.Errorf("wrong SOCKS5 IPv6 datagram: %x", packet[:n])
			}
			if _, err := udp.WriteToUDPAddrPort(append(header, "reply"...), from); err != nil {
				return err
			}
			_, err = io.ReadAll(control)
			return err
		}()
	}()

	t.Setenv(ipmiChildEnv, "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{host.String(), "0x1861", "6", executable}, ipmiChildArgs...)
	var stdout, stderr bytes.Buffer
	err = Run(server.Addr().String(), "ipmi-native", args, nil, &stdout, &stderr)
	skipBlockedLoopback(t, err)
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 17 {
		t.Fatalf("expected native exit 17; got %v; %s", err, &stderr)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), []byte{0, 0xff}) {
		t.Fatalf("stdout = %x; want 00ff", stdout.Bytes())
	}
}

func TestIPMIPortMatchesNativeBases(t *testing.T) {
	for input, want := range map[string]uint16{"0": 623, "623": 623, "01157": 623, "0x26f": 623, "65535": 65535} {
		port, err := parseIPMIPort(input)
		if err != nil || port != want {
			t.Errorf("parseIPMIPort(%q) = %d, %v; want %d", input, port, err, want)
		}
	}
	for _, input := range []string{"-1", "65536", "080", "0b1", "6_23"} {
		if _, err := parseIPMIPort(input); err == nil {
			t.Errorf("parseIPMIPort(%q) accepted invalid port", input)
		}
	}
}
