//go:build darwin || linux

package terminal

import (
	"bufio"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func open(input, output *os.File) *Terminal {
	inFD, outFD := int(input.Fd()), int(output.Fd())
	if _, err := unix.IoctlGetTermios(inFD, getTermios); err != nil {
		return nil
	}
	size, err := unix.IoctlGetWinsize(outFD, unix.TIOCGWINSZ)
	if err != nil || size.Col == 0 || size.Row == 0 {
		return nil
	}
	return &Terminal{
		Width:  int(size.Col),
		Height: int(size.Row),
		input:  bufio.NewReader(input),
		waitInput: func(timeout time.Duration) (bool, error) {
			deadline := time.Now().Add(timeout)
			fds := []unix.PollFd{{Fd: int32(inFD), Events: unix.POLLIN}}
			for time.Now().Before(deadline) {
				n, err := unix.Poll(fds, int(time.Until(deadline).Milliseconds())+1)
				if !errors.Is(err, unix.EINTR) {
					return n > 0, err
				}
			}
			return false, nil
		},
		start: func() (func() error, error) {
			old, err := unix.IoctlGetTermios(inFD, getTermios)
			if err != nil {
				return nil, err
			}
			mode := *old
			mode.Iflag &^= unix.IXON
			mode.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
			mode.Cc[unix.VMIN], mode.Cc[unix.VTIME] = 1, 0
			if err := unix.IoctlSetTermios(inFD, setTermios, &mode); err != nil {
				return nil, err
			}
			return func() error { return unix.IoctlSetTermios(inFD, setTermios, old) }, nil
		},
	}
}
