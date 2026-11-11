//go:build darwin || linux

package terminal

import (
	"bufio"
	"os"

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
