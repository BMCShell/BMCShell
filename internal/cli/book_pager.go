package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"bmcshell/internal/bookdb"
	"bmcshell/internal/render"
	"bmcshell/internal/terminal"
)

func (a *app) displayBook(query func(io.Writer, bookdb.Display) error) error {
	term := terminal.Open(a.in, a.out)
	if term == nil {
		return query(a.out, bookdb.Display{})
	}

	var output bookOutput
	defer output.close()
	var headerSize int64
	width := max(1, term.Width-1)
	queryErr := query(&output, bookdb.Display{
		Width: width,
		ListHeader: func(row string) error {
			n, err := io.WriteString(&output, row)
			headerSize = int64(n)
			return err
		},
	})
	source, err := output.reader()
	if err != nil {
		return errors.Join(queryErr, err)
	}
	if queryErr != nil || term.Height < 3 {
		_, err := io.Copy(a.out, source)
		return errors.Join(queryErr, err)
	}
	return showBookPages(a.out, source, term, width, headerSize)
}

// Keep short results in memory. Long queries can return many bodies, so spool
// larger results and retain only line offsets while paging.
type bookOutput struct {
	buf   bytes.Buffer
	file  *os.File
	spool *bufio.Writer
	size  int64
}

func (o *bookOutput) Write(p []byte) (int, error) {
	const memoryLimit = 256 << 10
	if o.spool == nil && len(p) <= memoryLimit-o.buf.Len() {
		n, err := o.buf.Write(p)
		o.size += int64(n)
		return n, err
	}
	if o.spool == nil {
		file, err := os.CreateTemp("", "bmcshell-book-*")
		if err != nil {
			return 0, err
		}
		o.file = file
		o.spool = bufio.NewWriter(file)
		if _, err := o.buf.WriteTo(o.spool); err != nil {
			return 0, err
		}
	}
	n, err := o.spool.Write(p)
	o.size += int64(n)
	return n, err
}

func (o *bookOutput) reader() (*io.SectionReader, error) {
	if o.file == nil {
		return io.NewSectionReader(bytes.NewReader(o.buf.Bytes()), 0, o.size), nil
	}
	if err := o.spool.Flush(); err != nil {
		return nil, err
	}
	return io.NewSectionReader(o.file, 0, o.size), nil
}

func (o *bookOutput) close() {
	if o.file != nil {
		o.file.Close()
		os.Remove(o.file.Name())
	}
}

type bookLineReader struct {
	source  *bufio.Reader
	width   int
	pending string
	offset  int64
	err     error
}

func (r *bookLineReader) next() (string, bool) {
	if r.pending == "" && r.err == nil {
		r.pending, r.err = r.source.ReadString('\n')
	}
	if r.pending == "" {
		return "", false
	}
	line := strings.TrimSuffix(r.pending, "\n")
	cells := 0
	for i, char := range line {
		width := render.RuneWidth(char)
		if cells > 0 && cells+width > r.width {
			r.offset += int64(i)
			r.pending = r.pending[i:]
			return line[:i], true
		}
		cells += width
	}
	r.offset += int64(len(r.pending))
	r.pending = ""
	return line, true
}

const bookIndexStride = 64

type bookScreen struct {
	source     *io.SectionReader
	width      int
	rows       int
	headerRows int
	offsets    []int64
}

// Store a checkpoint every 64 screen rows so the index stays small even for
// documents containing many short lines. Offsets may land inside a source line.
func indexBookScreen(source *io.SectionReader, width int, headerSize int64) (bookScreen, error) {
	screen := bookScreen{source: source, width: width}
	reader := bookLineReader{source: bufio.NewReader(source), width: width}
	for {
		offset := reader.offset
		if _, ok := reader.next(); !ok {
			break
		}
		if screen.rows%bookIndexStride == 0 {
			screen.offsets = append(screen.offsets, offset)
		}
		screen.rows++
		if reader.offset == headerSize {
			screen.headerRows = screen.rows
		}
	}
	if reader.err != io.EOF {
		return screen, reader.err
	}
	return screen, nil
}

func (screen bookScreen) writeRows(out *bufio.Writer, start, end int) error {
	if start == end {
		return nil
	}
	checkpoint := start / bookIndexStride
	offset := screen.offsets[checkpoint]
	source := io.NewSectionReader(screen.source, offset, screen.source.Size()-offset)
	reader := bookLineReader{source: bufio.NewReader(source), width: screen.width}
	for row := checkpoint * bookIndexStride; row < end; row++ {
		line, ok := reader.next()
		if !ok {
			return reader.err
		}
		if row < start {
			continue
		}
		if _, err := out.WriteString(line); err != nil {
			return err
		}
		if _, err := out.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return nil
}

func showBookPages(out io.Writer, source *io.SectionReader, term *terminal.Terminal, width int, headerSize int64) (err error) {
	screen, err := indexBookScreen(source, width, headerSize)
	if err != nil {
		return err
	}
	screenRows := term.Height - 1
	if screen.rows <= screenRows || term.Start() != nil {
		_, err := io.Copy(out, io.NewSectionReader(source, 0, source.Size()))
		return err
	}
	defer func() { err = errors.Join(err, term.Close()) }()
	if _, err := io.WriteString(out, "\x1b[?1049h"); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := io.WriteString(out, "\x1b[?1049l")
		err = errors.Join(err, restoreErr)
	}()

	headerRows := screen.headerRows
	if headerRows >= screenRows {
		headerRows = 0
	}
	pageRows := screenRows - headerRows
	last := screen.rows - pageRows
	writer := bufio.NewWriter(out)
	start := headerRows
	for {
		if _, err := writer.WriteString("\x1b[H\x1b[J"); err != nil {
			return err
		}
		if err := screen.writeRows(writer, 0, headerRows); err != nil {
			return err
		}
		if err := screen.writeRows(writer, start, start+pageRows); err != nil {
			return err
		}
		status := fmt.Sprintf("%d-%d/%d  Space/PgDn Next  PgUp Back  q Quit", start-headerRows+1, start+pageRows-headerRows, screen.rows-headerRows)
		if _, err := writer.WriteString(status[:min(width, len(status))]); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		for {
			key, err := term.ReadKey()
			if key == terminal.KeyQuit || errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			next := start
			switch key {
			case terminal.KeyNext:
				next += pageRows
			case terminal.KeyPrevious:
				next -= pageRows
			case terminal.KeyLineDown:
				next++
			case terminal.KeyLineUp:
				next--
			case terminal.KeyFirst:
				next = headerRows
			case terminal.KeyLast:
				next = last
			}
			next = min(last, max(headerRows, next))
			if next != start {
				start = next
				break
			}
		}
	}
}
