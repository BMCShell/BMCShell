package terminal

import (
	"bufio"
	"io"
	"os"
	"time"
)

type Key uint8

const (
	KeyUnknown Key = iota
	KeyNext
	KeyPrevious
	KeyLineDown
	KeyLineUp
	KeyFirst
	KeyLast
	KeyQuit
)

// Terminal uses an interactive input and output pair.
type Terminal struct {
	Width, Height int
	input         *bufio.Reader
	waitInput     func(time.Duration) (bool, error)
	start         func() (func() error, error)
	restore       func() error
}

// Open inspects the streams without changing terminal settings.
func Open(in io.Reader, out io.Writer) *Terminal {
	input, inputFile := in.(*os.File)
	output, outputFile := out.(*os.File)
	if !inputFile || !outputFile || os.Getenv("TERM") == "dumb" {
		return nil
	}
	return open(input, output)
}

// Start enables immediate key reading and terminal control sequences.
func (t *Terminal) Start() error {
	if t.restore != nil {
		return nil
	}
	restore, err := t.start()
	if err != nil {
		return err
	}
	t.restore = restore
	return nil
}

// Close restores the terminal settings saved by Start.
func (t *Terminal) Close() error {
	if t.restore == nil {
		return nil
	}
	restore := t.restore
	t.restore = nil
	return restore()
}

// ReadKey reads a navigation key while the terminal is started.
func (t *Terminal) ReadKey() (Key, error) {
	key, err := t.input.ReadByte()
	if err != nil {
		return KeyUnknown, err
	}
	switch key {
	case ' ', 'f':
		return KeyNext, nil
	case 'b':
		return KeyPrevious, nil
	case 'j', '\r', '\n':
		return KeyLineDown, nil
	case 'k':
		return KeyLineUp, nil
	case 'g':
		return KeyFirst, nil
	case 'G':
		return KeyLast, nil
	case 'q', 'Q', '\x03', '\x04':
		return KeyQuit, nil
	case '\x1b':
		return t.readEscape()
	}
	return KeyUnknown, nil
}

func (t *Terminal) readEscape() (Key, error) {
	prefix, err := t.input.ReadByte()
	if err != nil {
		return KeyUnknown, err
	}
	if prefix != '[' && prefix != 'O' {
		return KeyUnknown, t.input.UnreadByte()
	}
	var parameters [16]byte
	count := 0
	for count < len(parameters) {
		ch, err := t.input.ReadByte()
		if err != nil {
			return KeyUnknown, err
		}
		switch ch {
		case 'A':
			return KeyLineUp, nil
		case 'B':
			return KeyLineDown, nil
		case 'C':
			return KeyNext, nil
		case 'D':
			return KeyPrevious, nil
		case 'H':
			return KeyFirst, nil
		case 'F':
			return KeyLast, nil
		case '~':
			switch string(parameters[:count]) {
			case "1", "7":
				return KeyFirst, nil
			case "4", "8":
				return KeyLast, nil
			case "5":
				return KeyPrevious, nil
			case "6":
				return KeyNext, nil
			}
			return KeyUnknown, nil
		}
		if ch >= 0x40 && ch <= 0x7e {
			return KeyUnknown, nil
		}
		if ch < 0x20 || ch > 0x3f {
			return KeyUnknown, t.input.UnreadByte()
		}
		parameters[count] = ch
		count++
	}
	return KeyUnknown, nil
}
