package terminal

import (
	"errors"
	"io"
	"time"
	"unicode"
	"unicode/utf8"
)

type MenuKey uint8

const (
	MenuUnknown MenuKey = iota
	MenuRune
	MenuUp
	MenuDown
	MenuLeft
	MenuRight
	MenuHome
	MenuEnd
	MenuEnter
	MenuEscape
	MenuInterrupt
	MenuBackspace
	MenuDelete
)

const escapeTimeout = 40 * time.Millisecond

// ReadMenuKey reads one editing key from a started terminal. The rune result
// is used only for MenuRune.
func (t *Terminal) ReadMenuKey() (MenuKey, rune, error) {
	r, size, err := t.input.ReadRune()
	if err != nil {
		return MenuUnknown, 0, err
	}
	switch r {
	case '\r', '\n':
		return MenuEnter, 0, nil
	case '\x03', '\x04':
		return MenuInterrupt, 0, nil
	case '\b', '\x7f':
		return MenuBackspace, 0, nil
	case '\x1b':
		key, err := t.readMenuEscape()
		return key, 0, err
	}
	if unicode.IsPrint(r) && (r != utf8.RuneError || size != 1) {
		return MenuRune, r, nil
	}
	return MenuUnknown, 0, nil
}

func (t *Terminal) readMenuEscape() (MenuKey, error) {
	prefix, err := t.readEscapeByte()
	if errors.Is(err, io.EOF) {
		return MenuEscape, nil
	}
	if err != nil {
		return MenuUnknown, err
	}
	if prefix != '[' && prefix != 'O' {
		return MenuEscape, t.input.UnreadByte()
	}
	var parameters [16]byte
	for i := range parameters {
		ch, err := t.readEscapeByte()
		if errors.Is(err, io.EOF) {
			return MenuUnknown, nil
		}
		if err != nil {
			return MenuUnknown, err
		}
		switch ch {
		case 'A':
			return MenuUp, nil
		case 'B':
			return MenuDown, nil
		case 'C':
			return MenuRight, nil
		case 'D':
			return MenuLeft, nil
		case 'H':
			return MenuHome, nil
		case 'F':
			return MenuEnd, nil
		case '~':
			switch string(parameters[:i]) {
			case "1", "7":
				return MenuHome, nil
			case "3":
				return MenuDelete, nil
			case "4", "8":
				return MenuEnd, nil
			}
			return MenuUnknown, nil
		}
		if ch >= 0x40 && ch <= 0x7e {
			return MenuUnknown, nil
		}
		if ch < 0x20 || ch > 0x3f {
			return MenuUnknown, t.input.UnreadByte()
		}
		parameters[i] = ch
	}
	return MenuUnknown, nil
}

func (t *Terminal) readEscapeByte() (byte, error) {
	if t.input.Buffered() == 0 && t.waitInput != nil {
		ready, err := t.waitInput(escapeTimeout)
		if err != nil {
			return 0, err
		}
		if !ready {
			return 0, io.EOF
		}
	}
	return t.input.ReadByte()
}
