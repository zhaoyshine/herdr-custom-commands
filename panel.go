package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiInvert = "\033[7m"
	ansiRed    = "\033[31m"
)

const (
	keyUpRune   = "k"
	keyDownRune = "j"
	keyTopRune  = "g"
	keyEndRune  = "G"
	keyAddRune  = "a"
	keyCopyRune = "c"
	keyDel      = "d"
	keyAltDel   = "x"
)

const outputTailBytes = 60000
const defaultOutputTimeout = 30
const maxOutputTimeout = 86400

// panel is the persistent command list.
//
// A pick is handed to the run action instead of being started here, so no
// command is tied to this pane's session and killed with the pane.
type panel struct {
	env  env
	term *terminal
	ctx  invocationContext

	cmds    []string
	lineNos []int

	sel   int
	top   int
	hover int

	runCmd       string
	notice       string
	noticeOK     bool
	outHideAt    time.Time
	outHideArmed bool
	outDismissed bool
	titleDir     string
	dirTick      int
	dirty        bool

	rows        int
	cols        int
	lastCols    int
	settledCols int

	listH       int
	vis         int
	addRow      int
	listEndRow  int
	showOut     bool
	outH        int
	outHdrRow   int
	outContentH int

	outTimeout int
	marker     string
	lastFrame  []byte
}

func newPanel(e env) *panel {
	return &panel{
		env:          e,
		ctx:          parseContext(e.context),
		hover:        -1,
		dirty:        true,
		listH:        1,
		rows:         24,
		cols:         80,
		outTimeout:   outputTimeout(e),
		titleDir:     "",
		outHideAt:    time.Time{},
		outHideArmed: false,
	}
}

// outputTimeout bounds the delay so a hand-edited file cannot overflow the
// duration the countdown is built from.
func outputTimeout(e env) int {
	return readConfigInt(e.timeoutFile(), defaultOutputTimeout, 0, maxOutputTimeout)
}

func (p *panel) run() int {
	p.term = openTerminal()
	saved := saveModes()
	p.term.cleanupOnExit(saved)

	if p.duplicatePanel() {
		return 0
	}

	p.term.write(seqAltScreen + seqCursorOff)
	// Autowrap off keeps one logical row on one screen row, which the mouse row
	// mapping needs.
	p.term.write(seqNoWrap + seqMouseOn)
	p.term.raw()
	_ = p.term.flush()

	p.rows, p.cols = size()
	if p.cols < 20 {
		p.cols = 20
	}

	p.load()
	p.titleDir = p.execCwd()
	p.marker = p.env.markerFile(p.env.paneID)
	_ = os.Remove(p.env.legacyMarker())
	if f, err := os.Create(p.marker); err == nil {
		_ = f.Close()
	}

	for {
		if p.commandsChanged() {
			p.load()
			p.touchMarker()
			if p.sel > len(p.cmds) {
				p.sel = len(p.cmds)
			}
			if p.hover >= len(p.cmds) {
				p.hover = -1
			}
			p.dirty = true
		}
		if p.dirty {
			p.draw()
			p.dirty = false
			// Only a width that survives two draws is worth remembering: the
			// startup resize churns it.
			if p.cols != p.lastCols {
				p.lastCols = p.cols
			} else if p.cols != p.settledCols {
				p.settledCols = p.cols
				p.recordWidth()
			}
		}

		ev, err := p.term.readEvent(time.Second)
		if errors.Is(err, errClosed) {
			return 0
		}
		if errors.Is(err, errTimeout) {
			p.tick()
			continue
		}
		if ev.mouse != nil {
			p.handleMouse(ev.mouse)
		} else {
			p.handleKey(ev.key)
		}
	}
}

func (p *panel) tick() {
	// The focused pane moves the run directory; sampling every few ticks keeps
	// the Herdr CLI calls rare.
	p.dirTick++
	if p.dirTick >= 5 {
		p.dirTick = 0
		if dir := p.execCwd(); dir != p.titleDir {
			p.titleDir = dir
		}
	}
	if p.showOut && p.outTimeout > 0 && fileExists(p.env.runExitFile()) {
		now := time.Now()
		if !p.outHideArmed {
			p.outHideAt = now.Add(time.Duration(p.outTimeout) * time.Second)
			p.outHideArmed = true
		} else if !now.Before(p.outHideAt) {
			p.outDismissed = true
		}
	}
	p.dirty = true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (p *panel) duplicatePanel() bool {
	if p.env.paneID == "" || p.env.spaceID == "" {
		return false
	}
	panes, err := p.env.panes(p.env.spaceID)
	if err != nil {
		return false
	}
	for _, q := range panes {
		if q.Label == panelLabel && q.PaneID != p.env.paneID {
			return true
		}
	}
	return false
}

// execCwd is where a click would run. A pane restored from a snapshot may carry
// no workspace cwd, so the focused pane is the fallback.
func (p *panel) execCwd() string {
	for _, dir := range []string{p.ctx.WorkspaceCwd, p.ctx.FocusedPaneCwd} {
		if isDir(dir) {
			return dir
		}
	}
	if p.env.spaceID != "" {
		if panes, err := p.env.panes(p.env.spaceID); err == nil {
			for _, q := range panes {
				// A click focuses the panel, so the focused pane can be this
				// one: its directory is the plugin's own.
				if q.PaneID == p.env.paneID || q.Label == panelLabel {
					continue
				}
				if q.Focused && isDir(q.Cwd) {
					return q.Cwd
				}
			}
		}
	}
	return home()
}

func isDir(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (p *panel) load() {
	p.cmds = nil
	p.lineNos = nil
	raw, err := os.ReadFile(p.env.commandsFile())
	if err != nil {
		return
	}
	for i, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p.cmds = append(p.cmds, line)
		p.lineNos = append(p.lineNos, i+1)
	}
}

// The marker is per pane: a shared one would be refreshed by whichever panel
// reloads first, hiding the change from the rest.
func (p *panel) commandsChanged() bool {
	info, err := os.Stat(p.env.commandsFile())
	if err != nil {
		return false
	}
	marker, err := os.Stat(p.marker)
	if err != nil {
		return true
	}
	return info.ModTime().After(marker.ModTime())
}

func (p *panel) touchMarker() {
	now := time.Now()
	// Chtimes alone cannot bring back a marker removed from outside, and a
	// missing one reads as a change on every pass.
	if f, err := os.Create(p.marker); err == nil {
		_ = f.Close()
	}
	_ = os.Chtimes(p.marker, now, now)
}

func (p *panel) draw() {
	p.rows, p.cols = size()
	if p.cols < 20 {
		p.cols = 20
	}

	exitMarker := ""
	if raw, err := os.ReadFile(p.env.runExitFile()); err == nil {
		exitMarker = strings.TrimSpace(string(raw))
	}
	if p.runCmd == "" {
		if raw, err := os.ReadFile(p.env.runCmdFile()); err == nil {
			p.runCmd = strings.TrimSpace(string(raw))
		}
	}

	p.showOut = false
	if !p.outDismissed && fileNonEmpty(p.env.runLog()) {
		p.showOut = true
		p.outH = (p.rows - 3) / 3
		if p.outH < 5 {
			p.outH = 5
		}
		if p.outH > p.rows-7 {
			p.outH = p.rows - 7
		}
		if p.outH < 5 {
			p.showOut = false
			p.outH = 0
		}
	} else {
		p.outH = 0
	}

	p.listH = p.rows - 3 - p.outH
	if p.listH < 1 {
		p.listH = 1
	}

	rows := len(p.cmds) + 1
	if p.sel < 0 {
		p.sel = 0
	}
	if p.sel > rows-1 {
		p.sel = rows - 1
	}
	if p.top > p.sel {
		p.top = p.sel
	}
	if p.sel >= p.top+p.listH {
		p.top = p.sel - p.listH + 1
	}
	if p.top > rows-p.listH {
		p.top = rows - p.listH
	}
	if p.top < 0 {
		p.top = 0
	}

	// The add row and the output area above it are pinned, so neither moves as
	// commands are added or removed.
	p.vis = len(p.cmds) - p.top
	if p.vis > p.listH {
		p.vis = p.listH
	}
	if p.vis < 0 {
		p.vis = 0
	}
	p.addRow = p.rows
	p.listEndRow = p.rows - p.outH - 1
	p.outHdrRow = p.rows - p.outH + 1
	p.outContentH = p.outH - 2

	f := newFrame()

	switch {
	case p.notice != "" && p.noticeOK:
		f.row(" " + ansiDim + truncate(p.notice, p.cols-3) + ansiReset)
	case p.notice != "":
		f.row(" " + ansiRed + "! " + truncate(p.notice, p.cols-4) + ansiReset)
	case p.runCmd != "" && exitMarker == "":
		f.row(" " + ansiBold + truncate("running: "+p.runCmd, p.cols-3) + ansiReset)
	default:
		f.row(" " + ansiBold + shortPath(p.titleDir, p.cols-3) + ansiReset)
	}
	rule := strings.Repeat("─", p.cols)
	f.row(rule)

	for i := p.top; i < p.top+p.vis; i++ {
		label := truncate(" "+p.cmds[i], p.cols-3)
		row := pad(label, p.cols-1)
		if i == p.hover {
			f.row(ansiInvert + row + ansiRed + "×" + ansiReset)
		} else {
			f.row(row + ansiRed + "×" + ansiReset)
		}
	}
	for i := p.vis; i < p.listH; i++ {
		f.row("")
	}

	if p.showOut {
		f.row(rule)
		header := pad(truncate("output: "+p.runCmd, p.cols-14), p.cols-10)
		f.row(" " + ansiBold + header + ansiReset + ansiDim + "copy hide" + ansiReset)
		contentH := p.outContentH
		if exitMarker != "" {
			contentH--
		}
		if contentH < 0 {
			contentH = 0
		}
		shown := 0
		if contentH > 0 {
			if lines, err := tailLines(p.env.runLog(), contentH); err == nil {
				for _, line := range lines {
					f.row(" " + truncate(line, p.cols-2))
					shown++
				}
			}
		}
		for ; shown < contentH; shown++ {
			f.row("")
		}
		if exitMarker != "" {
			f.row(" " + ansiDim + "── exited " + exitMarker + " ──" + ansiReset)
		}
	}

	label := truncate(" + Add Command", p.cols-1)
	row := pad(label, p.cols)
	if p.hover == len(p.cmds) {
		f.row(ansiInvert + row + ansiReset)
	} else {
		f.row(row)
	}

	// An idle panel draws the same frame every tick: writing it again is a
	// full repaint on the far terminal for nothing.
	if bytes.Equal(f.bytes(), p.lastFrame) {
		return
	}
	p.lastFrame = append(p.lastFrame[:0], f.bytes()...)

	p.term.write(seqHome)
	p.term.write(string(f.bytes()))
	// Erase to the end of the screen; writing past the last row would scroll.
	p.term.write(seqEraseDown)
	_ = p.term.flush()
}

func (p *panel) handleKey(key string) {
	p.notice = ""
	p.noticeOK = false
	p.dirty = true
	switch key {
	case keyEnter:
		if p.sel < len(p.cmds) {
			p.runCommand(p.sel)
		} else {
			p.openAdd()
		}
	case keyUp, keyUpRune:
		p.moveSel(-1)
	case keyDown, keyDownRune:
		p.moveSel(1)
	case keyHome, keyTopRune:
		p.sel = 0
	case keyEnd, keyEndRune:
		p.sel = len(p.cmds)
	case keyAddRune, keySpace:
		p.openAdd()
	case keyCopyRune:
		if p.showOut {
			p.copyOutput()
		}
	case keyDel, keyAltDel:
		if p.sel < len(p.cmds) {
			p.delete(p.sel)
		}
	}
}

// moveSel walks the selection, which stops at the add row.
func (p *panel) moveSel(delta int) {
	if next := p.sel + delta; next >= 0 && next <= len(p.cmds) {
		p.sel = next
	}
}

// Button codes are raw SGR values: 35 motion, 64 and 65 the wheel, 0 a press.
func (p *panel) handleMouse(m *mouseEvent) {
	if m.button == 0 {
		p.notice = ""
		p.noticeOK = false
	}
	if m.button == 35 {
		idx := -1
		if m.y >= 3 && m.y <= p.listEndRow {
			idx = p.top + m.y - 3
			if idx >= p.top+p.vis {
				idx = -1
			}
		} else if m.y == p.addRow {
			idx = len(p.cmds)
		}
		if idx != p.hover {
			p.hover = idx
			p.dirty = true
		}
		return
	}
	if m.release {
		return
	}
	switch m.button {
	case 64:
		p.moveSel(-1)
		p.dirty = true
		return
	case 65:
		p.moveSel(1)
		p.dirty = true
		return
	case 0:
	default:
		return
	}
	if p.showOut && m.y >= p.outHdrRow && m.y < p.addRow {
		if m.y == p.outHdrRow {
			switch {
			case m.x >= p.cols-3:
				p.outDismissed = true
				p.dirty = true
			case m.x >= p.cols-8:
				p.copyOutput()
			}
		}
		return
	}
	if m.y == p.addRow {
		p.openAdd()
		return
	}
	if m.y >= 3 && m.y <= p.listEndRow {
		idx := p.top + m.y - 3
		if idx >= len(p.cmds) {
			return
		}
		p.sel = idx
		p.dirty = true
		if m.x >= p.cols-2 {
			p.delete(idx)
		} else {
			p.runCommand(idx)
		}
	}
}

func (p *panel) runCommand(idx int) {
	p.term.flush()
	payload := fmt.Sprintf("%s\n%s\n%d\n", p.execCwd(), p.cmds[idx], time.Now().Unix())
	// The hand-over goes first: a failed write must not throw away the output
	// the panel is still showing.
	if err := writeFileAtomic(p.env.pendingFile(), payload); err != nil {
		p.notice = "cannot write " + p.env.pendingFile()
		p.noticeOK = false
		p.dirty = true
		return
	}
	_ = os.Remove(p.env.runLog())
	_ = os.RemoveAll(p.env.runExitFile())
	p.outDismissed = false
	p.outHideArmed = false
	p.runCmd = p.cmds[idx]
	p.notice = ""
	p.noticeOK = false
	if err := p.env.invokeRunAction(); err != nil {
		// A payload left behind is run by the next invoke of the action, and
		// the panel must not claim a run it never started.
		_ = os.Remove(p.env.pendingFile())
		p.runCmd = ""
		p.outDismissed = true
		p.notice = "cannot start the run action"
	}
	p.dirty = true
}

func (p *panel) delete(idx int) {
	want := p.lineNos[idx]
	raw, err := os.ReadFile(p.env.commandsFile())
	if err != nil {
		return
	}
	lines := strings.Split(string(raw), "\n")
	// The list this row came from may be older than the file: the other panel,
	// or the user's editor, can have written it since. Delete the command the
	// row shows, or nothing.
	if want < 1 || want > len(lines) || strings.TrimSuffix(lines[want-1], "\r") != p.cmds[idx] {
		p.load()
		p.dirty = true
		return
	}
	kept := make([]string, 0, len(lines)-1)
	for i, line := range lines {
		if i+1 == want {
			continue
		}
		kept = append(kept, line)
	}
	if err := writeFileAtomic(p.env.commandsFile(), strings.Join(kept, "\n")); err != nil {
		return
	}
	p.load()
	if p.sel >= len(p.cmds) {
		p.sel = len(p.cmds)
	}
	p.dirty = true
}

func (p *panel) openAdd() {
	p.notice = ""
	p.noticeOK = false
	if err := p.env.openPopup("add"); err != nil {
		p.notice = "cannot open the add popup"
	}
	p.dirty = true
}

// recordWidth stores the share of the tab a reopened pane should come back at.
func (p *panel) recordWidth() {
	if p.env.paneID == "" {
		return
	}
	paneWidth, tabWidth, err := p.env.layoutWidths(p.env.paneID)
	if err != nil || tabWidth <= 0 {
		return
	}
	permille := (paneWidth*1000 + tabWidth/2) / tabWidth
	if permille < 30 || permille > 900 {
		return
	}
	_ = writeFileAtomic(p.env.widthFile(), strconv.Itoa(permille)+"\n")
}

// copyOutput uses OSC 52: the panel captures the mouse, so drag-select is
// unavailable.
func (p *panel) copyOutput() {
	if !fileNonEmpty(p.env.runLog()) {
		return
	}
	raw, err := tailBytes(p.env.runLog(), outputTailBytes)
	if err != nil || len(raw) == 0 {
		return
	}
	p.term.write("\033]52;c;" + base64.StdEncoding.EncodeToString(raw) + "\a")
	p.notice = "output copied to the clipboard"
	p.noticeOK = true
	p.dirty = true
}

func writeFileAtomic(path, content string) error {
	// The temp name carries the pid so two panels writing the same file cannot
	// fill one temp between them, and the target keeps the mode it had.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fileNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

const (
	tailChunk = 8192
	tailLimit = 1 << 20
)

// tailLines returns the last n lines of a file. It reads backwards from the end
// until it has them, because the log is re-read on every draw while a command
// keeps appending to it.
func tailLines(path string, n int) ([]string, error) {
	if n < 1 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var buf []byte
	found := 0
	for start := info.Size(); start > 0; {
		step := int64(tailChunk)
		if step > start {
			step = start
		}
		start -= step
		block := make([]byte, step)
		if _, err := f.ReadAt(block, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		buf = append(block, buf...)
		found += bytes.Count(block, []byte{'\n'})
		if start == 0 || found > n || len(buf) >= tailLimit {
			break
		}
	}
	if len(buf) == 0 {
		return nil, nil
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func tailBytes(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	start := int64(0)
	if size > max {
		start = size - max
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
