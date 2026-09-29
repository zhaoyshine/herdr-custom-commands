package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	seqAltScreen = "\033[?1049h"
	seqNoWrap    = "\033[?7l"
	seqMouseOn   = "\033[?1000h\033[?1003h\033[?1006h"
	seqMouseOff  = "\033[?1003l\033[?1006l\033[?1000l"
	seqWrapOn    = "\033[?7h"
	seqCursorOff = "\033[?25l"
	seqAltOff    = "\033[?1049l"
	seqReset     = "\033[0m"
	seqEraseRow  = "\033[2K"
	seqEraseDown = "\033[J"
	seqHome      = "\033[H"
)

var errTimeout = errors.New("no input within the deadline")
var errClosed = errors.New("input closed")

// escapeGap is how long a sequence started by Escape waits for its next byte.
const escapeGap = 40 * time.Millisecond

// terminal owns the pane's tty; every method is safe to call after close.
type terminal struct {
	out   *bufio.Writer
	input *byteReader
	once  sync.Once
}

func openTerminal() *terminal {
	return &terminal{
		out:   bufio.NewWriter(os.Stdout),
		input: newByteReader(os.Stdin),
	}
}

func (t *terminal) raw() {
	_, _ = stty("-echo", "-icanon", "min", "1", "time", "0")
}

func saveModes() string {
	out, err := stty("-g")
	if err != nil {
		return ""
	}
	return out
}

func (t *terminal) cleanupOnExit(saved string) {
	installCleanup(func() {
		t.write(seqMouseOff + seqWrapOn)
		t.write(seqReset + seqAltOff)
		t.restore(saved)
	})
}

func (t *terminal) restore(saved string) {
	t.once.Do(func() {
		_ = t.flush()
		if saved != "" {
			if _, err := stty(saved); err == nil {
				return
			}
		}
		_, _ = stty("sane")
	})
}

func (t *terminal) write(s string) {
	_, _ = t.out.WriteString(s)
}

func (t *terminal) flush() error {
	if t.out == nil {
		return nil
	}
	return t.out.Flush()
}

func size() (rows, cols int) {
	rows, cols = 24, 80
	out, err := stty("size")
	if err != nil {
		return rows, cols
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return rows, cols
	}
	if n, err := strconv.Atoi(fields[0]); err == nil && n > 0 {
		rows = n
	}
	if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
		cols = n
	}
	return rows, cols
}

func stty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

type event struct {
	key   string
	mouse *mouseEvent
}

type mouseEvent struct {
	button  int
	x       int
	y       int
	release bool
}

// byteReader turns a blocking stream into timed reads. A goroutine owns the
// stream and hands over whatever it gets; the caller never blocks on the tty
// itself, which is what makes the panel's one-second ticks possible.
type byteReader struct {
	chunks chan []byte
	buf    []byte
	err    error
}

func newByteReader(r io.Reader) *byteReader {
	b := &byteReader{chunks: make(chan []byte, 32)}
	go func() {
		defer close(b.chunks)
		for {
			chunk := make([]byte, 512)
			n, err := r.Read(chunk)
			if n > 0 {
				b.chunks <- chunk[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	return b
}

func (b *byteReader) readByte(timeout time.Duration) (byte, error) {
	if len(b.buf) > 0 {
		return b.pop(), nil
	}
	if b.err != nil {
		return 0, b.err
	}
	if timeout <= 0 {
		select {
		case chunk, ok := <-b.chunks:
			if !ok {
				b.err = errClosed
				return 0, errClosed
			}
			b.buf = chunk
			return b.pop(), nil
		default:
			return 0, errTimeout
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case chunk, ok := <-b.chunks:
		if !ok {
			b.err = errClosed
			return 0, errClosed
		}
		b.buf = chunk
		return b.pop(), nil
	case <-timer.C:
		return 0, errTimeout
	}
}

func (b *byteReader) pop() byte {
	c := b.buf[0]
	b.buf = b.buf[1:]
	return c
}

// Enter arrives as a newline, not a carriage return: raw mode turns off line
// editing but leaves ICRNL alone, so the tty maps one to the other.
func (t *terminal) readEvent(timeout time.Duration) (*event, error) {
	c, err := t.input.readByte(timeout)
	if err != nil {
		return nil, err
	}
	switch c {
	case '\r', '\n':
		return &event{key: keyEnter}, nil
	case ' ':
		return &event{key: keySpace}, nil
	case 0x1b:
		return t.readEscape()
	}
	return &event{key: t.readRune(c)}, nil
}

func (t *terminal) readRune(lead byte) string {
	width := 1
	switch {
	case lead&0xe0 == 0xc0:
		width = 2
	case lead&0xf0 == 0xe0:
		width = 3
	case lead&0xf8 == 0xf0:
		width = 4
	}
	if width == 1 {
		return string(lead)
	}
	buf := []byte{lead}
	for len(buf) < width {
		b, err := t.input.readByte(escapeGap)
		if err != nil {
			break
		}
		buf = append(buf, b)
	}
	return string(buf)
}

const (
	keyEnter = "ENTER"
	keySpace = "SPACE"
	keyEsc   = "ESC"
	keyUp    = "UP"
	keyDown  = "DOWN"
	keyLeft  = "LEFT"
	keyRight = "RIGHT"
	keyHome  = "HOME"
	keyEnd   = "END"
	keyNone  = "NONE"
)

// readEscape consumes a whole escape sequence. A sequence longer than the ones
// decoded here is still consumed: the bytes after the one that identified it
// would otherwise come back as typed input.
func (t *terminal) readEscape() (*event, error) {
	// A real sequence arrives as one write, so the next byte is already waiting
	// when the terminal sent more than Escape.
	next, err := t.input.readByte(escapeGap)
	if err != nil {
		return &event{key: keyEsc}, nil
	}
	if next == 'O' {
		return &event{key: t.readSS3()}, nil
	}
	if next != '[' {
		return &event{key: keyNone}, nil
	}
	var params [12]byte
	n := 0
	for {
		c, err := t.input.readByte(escapeGap)
		if err != nil {
			return &event{key: keyNone}, nil
		}
		if c >= 0x30 && c <= 0x3f {
			if n < len(params) {
				params[n] = c
				n++
			}
			continue
		}
		if c == 'M' || c == 'm' {
			if n > 0 && params[0] == '<' {
				return &event{mouse: mouseFromParams(params[1:n], c == 'm')}, nil
			}
			return &event{key: keyNone}, nil
		}
		return &event{key: csiKey(c, params[:n])}, nil
	}
}

// readSS3 decodes the ESC O form, which some terminals use for the arrows and
// the home/end keys.
func (t *terminal) readSS3() string {
	c, err := t.input.readByte(escapeGap)
	if err != nil {
		return keyNone
	}
	return csiKey(c, nil)
}

func csiKey(final byte, params []byte) string {
	switch final {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	case 'C':
		return keyRight
	case 'D':
		return keyLeft
	case 'H':
		return keyHome
	case 'F':
		return keyEnd
	case '~':
		switch firstParam(params) {
		case 1, 7:
			return keyHome
		case 4, 8:
			return keyEnd
		}
	}
	return keyNone
}

// firstParam is the number before the first ';' of a CSI.
func firstParam(params []byte) int {
	n := 0
	for _, c := range params {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// mouseFromParams decodes the SGR body: button ; x ; y.
func mouseFromParams(body []byte, release bool) *mouseEvent {
	values := [3]int{}
	field := 0
	for _, c := range body {
		switch {
		case c == ';':
			field++
		case c >= '0' && c <= '9' && field < 3:
			values[field] = values[field]*10 + int(c-'0')
		}
	}
	return &mouseEvent{button: values[0], x: values[1], y: values[2], release: release}
}

// frame joins the rows of one screen update with newlines and never terminates
// the last one, so a full-height frame cannot push the terminal into scrolling.
type frame struct {
	buf   []byte
	first bool
}

func newFrame() *frame {
	return &frame{first: true}
}

func (f *frame) row(s string) {
	if !f.first {
		f.buf = append(f.buf, '\n')
	}
	f.first = false
	f.buf = append(f.buf, seqEraseRow...)
	f.buf = append(f.buf, s...)
}

func (f *frame) bytes() []byte { return f.buf }
