package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"bmcshell/internal/render"
	"bmcshell/internal/terminal"
)

type settingUI struct {
	term  *terminal.Terminal
	input *bufio.Reader
	out   io.Writer
}

func newSettingUI(in io.Reader, out io.Writer) *settingUI {
	ui := &settingUI{term: terminal.Open(in, out), input: bufio.NewReader(in), out: out}
	if ui.term != nil {
		if err := ui.term.Start(); err != nil {
			ui.term = nil
		} else {
			fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
		}
	}
	return ui
}

func (ui *settingUI) close() error {
	if ui.term == nil {
		return nil
	}
	_, err := fmt.Fprint(ui.out, "\x1b[?25h\x1b[?1049l")
	return errors.Join(err, ui.term.Close())
}

func settingText(value string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return '�'
		}
		return r
	}, value)
}

func settingWidth(value string, columns int) string {
	width := 0
	for i, r := range value {
		width += render.RuneWidth(r)
		if width > columns {
			return value[:i]
		}
	}
	return value
}

func (ui *settingUI) page(title, hint string, details []string, body []string, footer string) error {
	var output strings.Builder
	line := func(text string) {
		if ui.term != nil {
			text = settingWidth(text, max(1, ui.term.Width-1))
		}
		output.WriteString(text)
		output.WriteString("\r\n")
	}
	if ui.term != nil {
		output.WriteString("\x1b[H\x1b[2J")
	} else {
		output.WriteByte('\n')
	}
	line(settingText(title))
	if ui.term != nil {
		output.WriteString("\x1b[90m")
	}
	line(settingText(hint))
	if ui.term != nil {
		output.WriteString("\x1b[0m")
	}
	line("")
	for _, detail := range details {
		line(settingText(detail))
	}
	if len(details) > 0 {
		line("")
	}
	for _, row := range body {
		line(row)
	}
	line("")
	line(footer)
	_, err := io.WriteString(ui.out, output.String())
	return err
}

// choose keeps the selected item and its hint visible even in a short terminal.
func (ui *settingUI) choose(title string, details []string, options []settingOption, cursor int, back string) (int, error) {
	if len(options) == 0 {
		return -1, nil
	}
	cursor = min(len(options)-1, max(0, cursor))
	for {
		first, last := 0, len(options)
		if ui.term != nil {
			rows := max(1, ui.term.Height-7-len(details))
			first = min(max(0, cursor-rows+1), max(0, len(options)-rows))
			last = min(len(options), first+rows)
		}
		body := make([]string, 0, last-first)
		for i := first; i < last; i++ {
			prefix := "    "
			if cursor == i {
				prefix = "  ▸ "
			}
			body = append(body, fmt.Sprintf("%s%d. %s", prefix, i+1, settingText(options[i].label)))
		}
		footer := "↑ ↓ 移动    Enter 确认    Esc " + back
		if ui.term == nil {
			footer = "输入编号确认，回车" + back + "："
		}
		if err := ui.page(title, options[cursor].hint, details, body, footer); err != nil {
			return -1, err
		}
		if ui.term == nil {
			line, err := ui.readLine()
			if err != nil {
				return -1, err
			}
			line = strings.TrimSpace(line)
			if line == "" || line == "0" {
				return -1, nil
			}
			if choice, err := strconv.Atoi(line); err == nil && choice >= 1 && choice <= len(options) {
				return choice - 1, nil
			}
			fmt.Fprintln(ui.out, "请输入菜单中的编号。")
			continue
		}
		key, ch, err := ui.term.ReadMenuKey()
		if err != nil {
			return -1, err
		}
		switch key {
		case terminal.MenuUp:
			cursor = (cursor + len(options) - 1) % len(options)
		case terminal.MenuDown:
			cursor = (cursor + 1) % len(options)
		case terminal.MenuHome:
			cursor = 0
		case terminal.MenuEnd:
			cursor = len(options) - 1
		case terminal.MenuEnter:
			return cursor, nil
		case terminal.MenuEscape:
			return -1, nil
		case terminal.MenuInterrupt:
			return -1, io.EOF
		case terminal.MenuRune:
			if ch >= '1' && ch <= '9' && int(ch-'1') < len(options) {
				cursor = int(ch - '1')
			}
		}
	}
}

func (ui *settingUI) edit(title, hint string, details []string, initial string, number, signed bool, submit func(string) error) error {
	value := []rune(initial)
	cursor := len(value)
	var validation string
	for {
		first := 0
		if ui.term != nil {
			for first < cursor && settingWidth(string(value[first:cursor]), max(1, ui.term.Width-13)) != string(value[first:cursor]) {
				first++
			}
		}
		body := []string{"输入值    " + settingText(string(value[first:cursor])) + "▏" + settingText(string(value[cursor:]))}
		if validation != "" {
			body = append(body, "          "+settingText(validation))
		}
		footer := "Enter 确认输入    Esc 放弃本次输入"
		if ui.term == nil {
			footer = "输入新值并回车确认："
		}
		if err := ui.page(title, hint, details, body, footer); err != nil {
			return err
		}
		if ui.term == nil {
			line, err := ui.readLine()
			if err != nil {
				return err
			}
			value, cursor = []rune(line), len([]rune(line))
			if err := submit(line); err == nil {
				return nil
			} else {
				validation = err.Error()
			}
			continue
		}
		key, ch, err := ui.term.ReadMenuKey()
		if err != nil {
			return err
		}
		switch key {
		case terminal.MenuRune:
			minus := signed && ch == '-' && cursor == 0 && (len(value) == 0 || value[0] != '-')
			if number && !minus && (ch < '0' || ch > '9') {
				validation = "只能输入数字 0–9。"
				if signed {
					validation = "请输入数字，负数可在开头输入负号。"
				}
			} else if unicode.IsPrint(ch) && len(value) < 4096 {
				value = append(value, 0)
				copy(value[cursor+1:], value[cursor:])
				value[cursor] = ch
				cursor++
				validation = ""
			}
		case terminal.MenuLeft:
			cursor = max(0, cursor-1)
		case terminal.MenuRight:
			cursor = min(len(value), cursor+1)
		case terminal.MenuHome:
			cursor = 0
		case terminal.MenuEnd:
			cursor = len(value)
		case terminal.MenuBackspace:
			if cursor > 0 {
				cursor--
				value = append(value[:cursor], value[cursor+1:]...)
				validation = ""
			}
		case terminal.MenuDelete:
			if cursor < len(value) {
				value = append(value[:cursor], value[cursor+1:]...)
				validation = ""
			}
		case terminal.MenuEnter:
			if err := submit(string(value)); err == nil {
				return nil
			} else {
				validation = err.Error()
			}
		case terminal.MenuEscape:
			return nil
		case terminal.MenuInterrupt:
			return io.EOF
		}
	}
}

func (ui *settingUI) readLine() (string, error) {
	line, err := ui.input.ReadString('\n')
	if len(line) != 0 && errors.Is(err, io.EOF) {
		err = nil
	}
	return strings.TrimRight(line, "\r\n"), err
}
