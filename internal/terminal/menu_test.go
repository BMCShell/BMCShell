package terminal

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadMenuKey(t *testing.T) {
	term := &Terminal{input: bufio.NewReader(strings.NewReader("\x1b[A\x1bOB\x1b[C\x1b[D\x1b[1~\x1bOF\x1b[3~\r\b\x7f汉✓\x1bZ\x03\x04\x1b"))}
	want := []struct {
		key  MenuKey
		rune rune
	}{
		{key: MenuUp}, {key: MenuDown}, {key: MenuRight}, {key: MenuLeft},
		{key: MenuHome}, {key: MenuEnd}, {key: MenuDelete}, {key: MenuEnter},
		{key: MenuBackspace}, {key: MenuBackspace}, {key: MenuRune, rune: '汉'},
		{key: MenuRune, rune: '✓'}, {key: MenuEscape}, {key: MenuRune, rune: 'Z'},
		{key: MenuInterrupt}, {key: MenuInterrupt}, {key: MenuEscape},
	}
	for i, expected := range want {
		key, value, err := term.ReadMenuKey()
		if err != nil || key != expected.key || value != expected.rune {
			t.Fatalf("key %d = %v, %q, %v, want %v, %q", i, key, value, err, expected.key, expected.rune)
		}
	}
	if _, _, err := term.ReadMenuKey(); !errors.Is(err, io.EOF) {
		t.Fatalf("read after keys: %v", err)
	}
}

func TestMenuEscapeTimeout(t *testing.T) {
	waits := 0
	term := &Terminal{
		input: bufio.NewReader(strings.NewReader("\x1b")),
		waitInput: func(timeout time.Duration) (bool, error) {
			waits++
			if timeout <= 0 {
				t.Fatalf("unexpected timeout %s", timeout)
			}
			return false, nil
		},
	}
	key, _, err := term.ReadMenuKey()
	if err != nil || key != MenuEscape || waits != 1 {
		t.Fatalf("standalone escape = %v, %v after %d waits", key, err, waits)
	}
}
