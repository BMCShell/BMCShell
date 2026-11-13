package terminal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var kernel32 = windows.NewLazySystemDLL("kernel32.dll")
var peekConsoleInput = kernel32.NewProc("PeekConsoleInputW")
var readConsoleInput = kernel32.NewProc("ReadConsoleInputW")
var peekNamedPipe = kernel32.NewProc("PeekNamedPipe")

type consoleInputRecord struct {
	eventType uint16
	_         uint16
	keyDown   uint32
	_         [12]byte
}

func open(input, output *os.File) *Terminal {
	inHandle, outHandle := windows.Handle(input.Fd()), windows.Handle(output.Fd())
	var mode uint32
	if windows.GetConsoleMode(inHandle, &mode) != nil {
		return openBashTTY(input, output)
	}
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(outHandle, &info) != nil {
		return openBashTTY(input, output)
	}
	return &Terminal{
		Width:  int(info.Window.Right) - int(info.Window.Left) + 1,
		Height: int(info.Window.Bottom) - int(info.Window.Top) + 1,
		input:  bufio.NewReader(input),
		waitInput: func(timeout time.Duration) (bool, error) {
			return waitConsoleInput(inHandle, timeout)
		},
		start: func() (func() error, error) {
			var oldIn, oldOut uint32
			if err := windows.GetConsoleMode(inHandle, &oldIn); err != nil {
				return nil, err
			}
			if err := windows.GetConsoleMode(outHandle, &oldOut); err != nil {
				return nil, err
			}
			outMode := oldOut | windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
			if err := windows.SetConsoleMode(outHandle, outMode); err != nil {
				return nil, err
			}
			inMode := oldIn &^ (windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT)
			if err := windows.SetConsoleMode(inHandle, inMode|windows.ENABLE_VIRTUAL_TERMINAL_INPUT); err != nil {
				_ = windows.SetConsoleMode(outHandle, oldOut)
				return nil, err
			}
			return func() error {
				return errors.Join(windows.SetConsoleMode(inHandle, oldIn), windows.SetConsoleMode(outHandle, oldOut))
			}, nil
		},
	}
}

func openBashTTY(input, output *os.File) *Terminal {
	device, stty := os.Getenv("BMCSHELL_BOOK_TTY"), os.Getenv("BMCSHELL_BOOK_STTY")
	bash := os.Getenv("BMCSHELL_BOOK_BASH")
	number, ok := strings.CutPrefix(device, "/dev/pty")
	if !ok || number == "" || strings.Trim(number, "0123456789") != "" || stty == "" || bash == "" {
		return nil
	}
	if !bashTTYHandle(windows.Handle(input.Fd()), number) || !bashTTYHandle(windows.Handle(output.Fd()), number) {
		return nil
	}
	runStty := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// A native child does not inherit the foreground Bash process group.
		// Ignore SIGTTOU so stty can restore the foreground terminal's modes.
		base := []string{"--noprofile", "--norc", "-p", "-c", `trap '' TTOU && exec "$@"`, "bmcshell-stty", stty, "-F", device}
		cmd := exec.CommandContext(ctx, bash, append(base, args...)...)
		cmd.WaitDelay = time.Second
		return cmd.Output()
	}
	size, err := runStty("size")
	var height, width int
	if err != nil {
		return nil
	}
	if _, err := fmt.Sscan(string(size), &height, &width); err != nil || height <= 0 || width <= 0 {
		return nil
	}
	return &Terminal{
		Width:  width,
		Height: height,
		input:  bufio.NewReader(input),
		waitInput: func(timeout time.Duration) (bool, error) {
			return waitPipeInput(windows.Handle(input.Fd()), timeout)
		},
		start: func() (func() error, error) {
			old, err := runStty("-g")
			if err != nil {
				return nil, err
			}
			restore := func() error {
				_, err := runStty(strings.TrimSpace(string(old)))
				return err
			}
			if _, err := runStty("-icanon", "-echo", "-isig", "-iexten", "-ixon", "min", "1", "time", "0"); err != nil {
				_ = restore()
				return nil, err
			}
			return restore, nil
		},
	}
}

func waitConsoleInput(handle windows.Handle, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		event, err := windows.WaitForSingleObject(handle, uint32(time.Until(deadline).Milliseconds()+1))
		if err != nil {
			return false, err
		}
		if event == uint32(windows.WAIT_TIMEOUT) {
			return false, nil
		}
		var record consoleInputRecord
		var count uint32
		ok, _, err := peekConsoleInput.Call(uintptr(handle), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&count)))
		if ok == 0 {
			return false, err
		}
		if count == 0 {
			continue
		}
		if record.eventType == windows.KEY_EVENT && record.keyDown != 0 {
			return true, nil
		}
		// A key-up, mouse or focus event makes the handle ready, but
		// ReadConsole would skip it and wait indefinitely for another key.
		ok, _, err = readConsoleInput.Call(uintptr(handle), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&count)))
		if ok == 0 {
			return false, err
		}
	}
	return false, nil
}

func waitPipeInput(handle windows.Handle, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		var available uint32
		ok, _, err := peekNamedPipe.Call(uintptr(handle), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
		if ok == 0 {
			if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA) {
				return false, io.EOF
			}
			return false, err
		}
		if available != 0 {
			return true, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		time.Sleep(min(remaining, 5*time.Millisecond))
	}
}

// MSYS and Cygwin expose pseudoterminals as Windows named pipes.
func bashTTYHandle(handle windows.Handle, number string) bool {
	kind, err := windows.GetFileType(handle)
	if err != nil || kind != windows.FILE_TYPE_PIPE {
		return false
	}
	var info struct {
		length uint32
		name   [256]uint16
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileNameInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil || info.length > uint32(len(info.name)*2) {
		return false
	}
	name := windows.UTF16ToString(info.name[:info.length/2])
	return (strings.HasPrefix(name, `\msys-`) || strings.HasPrefix(name, `\cygwin-`)) &&
		strings.Contains(name, "-pty"+number+"-")
}
