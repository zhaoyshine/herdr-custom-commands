// Package terminal owns the pane's tty: raw mode, timed input decoding, and
// screen writes.
package terminal

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
	SeqAltScreen = "\033[?1049h"
	SeqNoWrap    = "\033[?7l"
	SeqMouseOn   = "\033[?1000h\033[?1003h\033[?1006h"
	SeqMouseOff  = "\033[?1003l\033[?1006l\033[?1000l"
	SeqWrapOn    = "\033[?7h"
	SeqCursorOff = "\033[?25l"
	SeqAltOff    = "\033[?1049l"
	SeqReset     = "\033[0m"
	SeqEraseRow  = "\033[2K"
	SeqEraseDown = "\033[J"
	SeqHome      = "\033[H"
)

var ErrTimeout = errors.New("no input within the deadline")
var ErrClosed = errors.New("input closed")

// escapeGap is how long a sequence started by Escape waits for its next byte.
const escapeGap = 40 * time.Millisecond

// Terminal owns the pane's tty; every method is safe to call after Shutdown.
// The signal handler writes from its own goroutine, so writes are serialized.
type Terminal struct {
	mu    sync.Mutex
	out   *bufio.Writer
	input *byteReader
	once  sync.Once
}

func Open() *Terminal {
	return &Terminal{
		out:   bufio.NewWriter(os.Stdout),
		input: newByteReader(os.Stdin),
	}
}

func (t *Terminal) Raw() {
	_, _ = stty("-echo", "-icanon", "min", "1", "time", "0")
}

func SaveModes() string {
	out, err := stty("-g")
	if err != nil {
		return ""
	}
	return out
}

// Shutdown tears the tty back down on the way out: mouse off, wrap and modes
// restored, alt screen left.
func (t *Terminal) Shutdown(saved string) {
	t.Write(SeqMouseOff + SeqWrapOn)
	t.Write(SeqReset + SeqAltOff)
	t.once.Do(func() {
		_ = t.Flush()
		if saved != "" {
			if _, err := stty(saved); err == nil {
				return
			}
		}
		_, _ = stty("sane")
	})
}

func (t *Terminal) Write(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.out.WriteString(s)
}

func (t *Terminal) Flush() error {
	if t.out == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.out.Flush()
}

func Size() (rows, cols int) {
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

type Event struct {
	Key   string
	Mouse *MouseEvent
}

type MouseEvent struct {
	Button  int
	X       int
	Y       int
	Release bool
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
				b.err = ErrClosed
				return 0, ErrClosed
			}
			b.buf = chunk
			return b.pop(), nil
		default:
			return 0, ErrTimeout
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case chunk, ok := <-b.chunks:
		if !ok {
			b.err = ErrClosed
			return 0, ErrClosed
		}
		b.buf = chunk
		return b.pop(), nil
	case <-timer.C:
		return 0, ErrTimeout
	}
}

func (b *byteReader) pop() byte {
	c := b.buf[0]
	b.buf = b.buf[1:]
	return c
}

// Enter arrives as a newline, not a carriage return: raw mode turns off line
// editing but leaves ICRNL alone, so the tty maps one to the other.
func (t *Terminal) ReadEvent(timeout time.Duration) (*Event, error) {
	c, err := t.input.readByte(timeout)
	if err != nil {
		return nil, err
	}
	switch c {
	case '\r', '\n':
		return &Event{Key: KeyEnter}, nil
	case ' ':
		return &Event{Key: KeySpace}, nil
	case 0x1b:
		return t.readEscape()
	}
	return &Event{Key: t.readRune(c)}, nil
}

func (t *Terminal) readRune(lead byte) string {
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
	KeyEnter = "ENTER"
	KeySpace = "SPACE"
	KeyEsc   = "ESC"
	KeyUp    = "UP"
	KeyDown  = "DOWN"
	KeyLeft  = "LEFT"
	KeyRight = "RIGHT"
	KeyHome  = "HOME"
	KeyEnd   = "END"
	KeyNone  = "NONE"
)

// readEscape consumes a whole escape sequence. A sequence longer than the ones
// decoded here is still consumed: the bytes after the one that identified it
// would otherwise come back as typed input.
func (t *Terminal) readEscape() (*Event, error) {
	// A real sequence arrives as one write, so the next byte is already waiting
	// when the terminal sent more than Escape.
	next, err := t.input.readByte(escapeGap)
	if err != nil {
		return &Event{Key: KeyEsc}, nil
	}
	if next == 'O' {
		return &Event{Key: t.readSS3()}, nil
	}
	if next != '[' {
		return &Event{Key: KeyNone}, nil
	}
	var params [12]byte
	n := 0
	for {
		c, err := t.input.readByte(escapeGap)
		if err != nil {
			return &Event{Key: KeyNone}, nil
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
				return &Event{Mouse: mouseFromParams(params[1:n], c == 'm')}, nil
			}
			return &Event{Key: KeyNone}, nil
		}
		return &Event{Key: csiKey(c, params[:n])}, nil
	}
}

// readSS3 decodes the ESC O form, which some terminals use for the arrows and
// the home/end keys.
func (t *Terminal) readSS3() string {
	c, err := t.input.readByte(escapeGap)
	if err != nil {
		return KeyNone
	}
	return csiKey(c, nil)
}

func csiKey(final byte, params []byte) string {
	switch final {
	case 'A':
		return KeyUp
	case 'B':
		return KeyDown
	case 'C':
		return KeyRight
	case 'D':
		return KeyLeft
	case 'H':
		return KeyHome
	case 'F':
		return KeyEnd
	case '~':
		switch firstParam(params) {
		case 1, 7:
			return KeyHome
		case 4, 8:
			return KeyEnd
		}
	}
	return KeyNone
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
func mouseFromParams(body []byte, release bool) *MouseEvent {
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
	return &MouseEvent{Button: values[0], X: values[1], Y: values[2], Release: release}
}

// Frame joins the rows of one screen update with newlines and never terminates
// the last one, so a full-height frame cannot push the terminal into scrolling.
type Frame struct {
	buf   []byte
	first bool
}

func NewFrame() *Frame {
	return &Frame{first: true}
}

func (f *Frame) Row(s string) {
	if !f.first {
		f.buf = append(f.buf, '\n')
	}
	f.first = false
	f.buf = append(f.buf, SeqEraseRow...)
	f.buf = append(f.buf, s...)
}

func (f *Frame) Bytes() []byte { return f.buf }
