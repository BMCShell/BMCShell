package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func skipBlockedLoopback(t *testing.T, err error) {
	t.Helper()
	var socketErr *net.OpError
	if !errors.As(err, &socketErr) {
		return
	}
	const wsaAccessDenied = syscall.Errno(10013)
	if errors.Is(err, os.ErrPermission) || runtime.GOOS == "windows" && errors.Is(err, wsaAccessDenied) {
		t.Skipf("loopback sockets are restricted: %v", err)
	}
}

func localServer(t *testing.T) *net.TCPListener {
	t.Helper()
	server, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		skipBlockedLoopback(t, err)
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return server
}

func receiveRequest(socket net.Conn, command byte, response netip.AddrPort) ([]byte, error) {
	if err := socket.SetDeadline(time.Now().Add(setupTimeout)); err != nil {
		return nil, err
	}
	greeting := make([]byte, 3)
	if _, err := io.ReadFull(socket, greeting); err != nil {
		return nil, err
	}
	if !bytes.Equal(greeting, []byte{5, 1, 0}) {
		return nil, errors.New("unexpected SOCKS5 greeting")
	}
	if _, err := socket.Write([]byte{5, 0}); err != nil {
		return nil, err
	}
	request := make([]byte, 10)
	if _, err := io.ReadFull(socket, request); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(request, []byte{5, command, 0, 1}) {
		return nil, errors.New("unexpected SOCKS5 request")
	}
	_, err := socket.Write(append([]byte{5, 0, 0}, wireAddress(response)...))
	return request[3:], err
}

func TestConnectRelaysBothStreams(t *testing.T) {
	server := localServer(t)
	destination := netip.MustParseAddrPort("192.0.2.40:22")
	request := []byte{0x00, 0x0a, 0xff}
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- func() error {
			socket, err := server.AcceptTCP()
			if err != nil {
				return err
			}
			defer socket.Close()
			target, err := receiveRequest(socket, connectCommand, netip.AddrPortFrom(loopback, 0))
			if err != nil {
				return err
			}
			if !bytes.Equal(target, wireAddress(destination)) {
				return errors.New("wrong CONNECT destination")
			}
			message, err := io.ReadAll(socket)
			if err != nil {
				return err
			}
			if !bytes.Equal(message, request) {
				return errors.New("wrong CONNECT payload")
			}
			_, err = socket.Write(message)
			return err
		}()
	}()
	var response bytes.Buffer
	err := Run(server.Addr().String(), "ssh", []string{destination.Addr().String(), "22"}, bytes.NewReader(request), &response, io.Discard)
	skipBlockedLoopback(t, err)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response.Bytes(), request) {
		t.Fatalf("response = %x; want %x", response.Bytes(), request)
	}
}

func TestUDPAssociationRelaysNativeChild(t *testing.T) {
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
	udpAddress := udp.LocalAddr().(*net.UDPAddr).AddrPort()
	target := netip.MustParseAddrPort("192.0.2.40:623")
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- func() error {
			control, err := server.AcceptTCP()
			if err != nil {
				return err
			}
			defer control.Close()
			clientAddress, err := receiveRequest(control, associateCommand, udpAddress)
			if err != nil {
				return err
			}
			packet := make([]byte, 100)
			n, from, err := udp.ReadFromUDPAddrPort(packet)
			if err != nil {
				return err
			}
			if !bytes.Equal(clientAddress, wireAddress(from)) {
				return errors.New("UDP ASSOCIATE used a different source socket")
			}
			header := append([]byte{0, 0, 0}, wireAddress(target)...)
			if !bytes.Equal(packet[:n], append(header, []byte("request")...)) {
				return fmt.Errorf("wrong UDP request: %x", packet[:n])
			}
			if _, err := udp.WriteToUDPAddrPort(append(header, []byte("reply")...), from); err != nil {
				return err
			}
			// Keep the association alive until the native process has exited.
			_, err = io.ReadAll(control)
			return err
		}()
	}()

	var stderr bytes.Buffer
	err = relayUDP(server.Addr().(*net.TCPAddr).AddrPort(), target, func(port uint16) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProxyNativeChild$")
		cmd.Env = append(os.Environ(), "BMCSHELL_PROXY_TEST_PORT="+strconv.Itoa(int(port)))
		cmd.Stderr = &stderr
		return cmd
	})
	skipBlockedLoopback(t, err)
	var childErr *exec.ExitError
	if !errors.As(err, &childErr) || childErr.ExitCode() != 17 {
		t.Fatalf("expected native exit 17; got %v; %s", err, &stderr)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestProxyNativeChild(t *testing.T) {
	port := os.Getenv("BMCSHELL_PROXY_TEST_PORT")
	if port == "" {
		return
	}
	socket, err := net.DialTimeout("udp4", "127.0.0.1:"+port, setupTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := socket.SetDeadline(time.Now().Add(setupTimeout)); err != nil {
		t.Fatal(err)
	}
	if _, err := socket.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 100)
	n, err := socket.Read(buffer)
	if err != nil || string(buffer[:n]) != "reply" {
		t.Fatalf("native UDP reply = %q, %v", buffer[:n], err)
	}
	os.Exit(17)
}

func TestIPMIRejectsBundledHostOverride(t *testing.T) {
	t.Setenv("IPMI_PASSWORD", "example")
	err := Run("127.0.0.1:1", "ipmi", []string{"192.0.2.40", os.Args[0], "user", "-vH192.0.2.50", "mc", "info"}, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "代理模式不支持该 IPMI 选项") {
		t.Fatalf("got %v; expected option rejection before connecting", err)
	}
}
