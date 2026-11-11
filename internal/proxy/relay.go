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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

// The V2 option allowlist excludes native host, port and credential overrides,
// including when getopt sees a bundle of short options.
var ipmiOption = regexp.MustCompile(`^-[46hVvcgsKYZ]+$|^-[46hVvcgsKYZ]*[odCLAtTmzSlbBekyORND].*$`)

// Run executes a V2-compatible SOCKS5 relay using a configured local endpoint.
// ssh relays the supplied streams. IPMI and SNMP modes run the supplied
// executable with its target replaced by a temporary local UDP listener.
func Run(address, mode string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	proxy, err := proxyAddress(address)
	if err != nil {
		return err
	}
	switch mode {
	case "ssh":
		if len(args) != 2 {
			return errors.New("用法 proxy ssh <IPv4> <端口>")
		}
		port, err := parsePort(args[1])
		if err != nil {
			return err
		}
		target, err := endpoint(args[0], port)
		if err != nil {
			return err
		}
		return relayTCP(proxy, target, stdin, stdout)
	case "ipmi-native":
		if len(args) < 4 || !filepath.IsAbs(args[3]) {
			return errors.New("用法 proxy ipmi-native <主机> <端口> <地址族> <可执行文件绝对路径> [参数 ...]")
		}
		port, err := parseIPMIPort(args[1])
		if err != nil {
			return err
		}
		target, err := ipmiEndpoint(args[0], port, args[2])
		if err != nil {
			return err
		}
		// The wrapper has already removed the native host and port options.
		return relayUDP(proxy, target, func(localPort uint16) *exec.Cmd {
			argv := append([]string{"-H", udpLoopback(target).String(), "-p", strconv.Itoa(int(localPort))}, args[4:]...)
			cmd := exec.Command(args[3], argv...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
			return cmd
		})
	case "ipmi", "snmp":
		if len(args) < 3 || !filepath.IsAbs(args[1]) {
			return fmt.Errorf("代理模式 %s 需要目标、可执行文件的绝对路径和工具参数", mode)
		}
		var port uint16 = defaultIPMIPort
		arguments := args[3:]
		insert, localFormat := 5, "%d"
		if mode == "ipmi" {
			if _, present := os.LookupEnv("IPMI_PASSWORD"); !present {
				return errors.New("须设置 IPMI_PASSWORD")
			}
			for _, arg := range arguments {
				if strings.HasPrefix(arg, "-") && !ipmiOption.MatchString(arg) {
					return errors.New("代理模式不支持该 IPMI 选项。主机、端口和凭据由 BMCShell 管理")
				}
			}
			arguments = append([]string{"-I", "lanplus", "-H", "127.0.0.1", "-p", "-U", args[2], "-E"}, arguments...)
		} else {
			port, localFormat = 161, "udp:127.0.0.1:%d"
			insert, err = strconv.Atoi(args[2])
			if err != nil || insert < 0 || insert > len(arguments) {
				return errors.New("SNMP 代理地址的参数位置无效")
			}
		}
		target, err := endpoint(args[0], port)
		if err != nil {
			return err
		}
		return relayUDP(proxy, target, func(localPort uint16) *exec.Cmd {
			argv := append([]string(nil), arguments[:insert]...)
			argv = append(argv, fmt.Sprintf(localFormat, localPort))
			argv = append(argv, arguments[insert:]...)
			cmd := exec.Command(args[1], argv...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
			return cmd
		})
	default:
		return fmt.Errorf("未知的 SOCKS5 转发模式 %q", mode)
	}
}

func relayTCP(proxy, target netip.AddrPort, stdin io.Reader, stdout io.Writer) error {
	control, _, err := openProxy(proxy, connectCommand, target)
	if err != nil {
		return err
	}
	defer control.Close()
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	inputError := make(chan error, 1)
	go func() {
		_, err := io.Copy(control, stdin)
		if err == nil {
			err = control.CloseWrite()
		}
		inputError <- err
		if err != nil {
			control.Close()
		}
	}()
	_, err = io.Copy(stdout, control)
	select {
	case writeErr := <-inputError:
		if writeErr != nil {
			return writeErr
		}
	default:
	}
	return err
}

func udpLoopback(target netip.AddrPort) netip.Addr {
	if target.Addr().Is6() {
		return netip.IPv6Loopback()
	}
	return loopback
}

func relayUDP(proxy, target netip.AddrPort, buildCommand func(uint16) *exec.Cmd) error {
	local, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(udpLoopback(target), 0)))
	if err != nil {
		return err
	}
	defer local.Close()
	relay, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(loopback, 0)))
	if err != nil {
		return err
	}
	defer relay.Close()

	control, reply, err := openProxy(proxy, associateCommand, relay.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		return err
	}
	defer control.Close()
	remote, err := loopbackRelay(reply, proxy)
	if err != nil {
		return err
	}

	cmd := buildCommand(local.LocalAddr().(*net.UDPAddr).AddrPort().Port())
	if err := cmd.Start(); err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() { result <- cmd.Wait() }()

	header := append([]byte{0, 0, 0}, wireAddress(target)...)
	var client atomic.Pointer[netip.AddrPort]
	failures := make(chan error, 3)
	go func() {
		var probe [1]byte
		_, err := control.Read(probe[:])
		if err == nil || errors.Is(err, io.EOF) {
			err = errors.New("SOCKS5 关闭或更改了 UDP 关联")
		}
		failures <- err
	}()
	go func() { failures <- sendPackets(local, relay, remote, header, &client) }()
	go func() { failures <- receivePackets(local, relay, remote, header, &client) }()
	select {
	case err := <-result:
		return err
	case err := <-failures:
		select {
		case childErr := <-result:
			return childErr
		default:
			_ = cmd.Process.Kill()
			<-result
			return fmt.Errorf("SOCKS5 UDP 转发失败：%w", err)
		}
	}
}

func sendPackets(local, relay *net.UDPConn, remote netip.AddrPort, header []byte, client *atomic.Pointer[netip.AddrPort]) error {
	packet := make([]byte, 65535)
	copy(packet, header)
	for {
		n, source, err := local.ReadFromUDPAddrPort(packet[len(header):])
		if err != nil {
			return err
		}
		expected := client.Load()
		if expected == nil {
			client.Store(&source)
		} else if source != *expected {
			continue
		}
		if _, err := relay.WriteToUDPAddrPort(packet[:len(header)+n], remote); err != nil {
			return err
		}
	}
}

func receivePackets(local, relay *net.UDPConn, remote netip.AddrPort, header []byte, client *atomic.Pointer[netip.AddrPort]) error {
	packet := make([]byte, 65535)
	for {
		n, source, err := relay.ReadFromUDPAddrPort(packet)
		if err != nil {
			return err
		}
		destination := client.Load()
		if source != remote || destination == nil || !bytes.HasPrefix(packet[:n], header) {
			continue
		}
		if _, err := local.WriteToUDPAddrPort(packet[len(header):n], *destination); err != nil {
			return err
		}
	}
}
