package panel

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhaoyshine/herdr-custom-commands/internal/app"
	"github.com/zhaoyshine/herdr-custom-commands/internal/env"
	"github.com/zhaoyshine/herdr-custom-commands/internal/fs"
	"github.com/zhaoyshine/herdr-custom-commands/internal/herdr"
	"github.com/zhaoyshine/herdr-custom-commands/internal/terminal"
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

// Panel is the persistent command list.
//
// A pick is handed to the run action instead of being started here, so no
// command is tied to this pane's session and killed with the pane.
type Panel struct {
	env   env.Env
	herdr *herdr.Client
	term  *terminal.Terminal
	ctx   herdr.Context

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
	followCwd    string
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

func New(e env.Env) *Panel {
	return &Panel{
		env:        e,
		herdr:      herdr.New(e.Herdr, env.PluginID),
		ctx:        herdr.ParseContext(e.Context),
		hover:      -1,
		dirty:      true,
		listH:      1,
		rows:       24,
		cols:       80,
		outTimeout: outputTimeout(e),
	}
}

// outputTimeout bounds the delay so a hand-edited file cannot overflow the
// duration the countdown is built from.
func outputTimeout(e env.Env) int {
	return env.ReadIntConfig(e.TimeoutFile(), defaultOutputTimeout, 0, maxOutputTimeout)
}

func (p *Panel) Run() int {
	p.term = terminal.Open()
	saved := terminal.SaveModes()
	app.OnCleanup(func() { p.term.Shutdown(saved) })

	if p.duplicatePanel() {
		return 0
	}

	p.term.Write(terminal.SeqAltScreen + terminal.SeqCursorOff)
	// Autowrap off keeps one logical row on one screen row, which the mouse row
	// mapping needs.
	p.term.Write(terminal.SeqNoWrap + terminal.SeqMouseOn)
	p.term.Raw()
	_ = p.term.Flush()

	p.rows, p.cols = terminal.Size()
	p.cols = max(p.cols, 20)

	p.load()
	p.titleDir = p.execCwd()
	p.marker = p.env.MarkerFile(p.env.PaneID)
	_ = os.Remove(p.env.LegacyMarker())
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
			if p.hover > len(p.cmds) {
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

		ev, err := p.term.ReadEvent(time.Second)
		if errors.Is(err, terminal.ErrClosed) {
			return 0
		}
		if errors.Is(err, terminal.ErrTimeout) {
			p.tick()
			continue
		}
		if ev.Mouse != nil {
			p.handleMouse(ev.Mouse)
		} else {
			p.handleKey(ev.Key)
		}
	}
}

func (p *Panel) tick() {
	// The focused pane moves the run directory; sampling every few ticks keeps
	// the Herdr CLI calls rare.
	p.dirTick++
	if p.dirTick >= 5 {
		p.dirTick = 0
		if dir := p.execCwd(); dir != p.titleDir {
			p.titleDir = dir
		}
	}
	if p.showOut && p.outTimeout > 0 && fs.Exists(p.env.RunExitFile()) {
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

func (p *Panel) duplicatePanel() bool {
	if p.env.PaneID == "" || p.env.SpaceID == "" {
		return false
	}
	panes, err := p.herdr.Panes(p.env.SpaceID)
	if err != nil {
		return false
	}
	for _, q := range panes {
		if q.Label == Label && q.PaneID != p.env.PaneID {
			return true
		}
	}
	return false
}

// execCwd is where a click would run. The live focused pane leads, because a
// cd in it moves the workspace's directory; the launch-time context is the
// fallback for a pane restored from a snapshot, which may carry no live cwd.
// A click focuses this panel and hides the user's pane behind it, so the last
// seen user cwd is cached and serves while the panel holds focus.
func (p *Panel) execCwd() string {
	if p.env.SpaceID != "" {
		if panes, err := p.herdr.Panes(p.env.SpaceID); err == nil {
			// A click focuses the panel, so the focused pane can be this
			// one: its directory is the plugin's own.
			if i := slices.IndexFunc(panes, func(q herdr.Pane) bool {
				return q.PaneID != p.env.PaneID && q.Label != Label && q.Focused && fs.IsDir(q.Cwd)
			}); i >= 0 {
				p.followCwd = panes[i].Cwd
				return panes[i].Cwd
			}
		}
	}
	if p.followCwd != "" && fs.IsDir(p.followCwd) {
		return p.followCwd
	}
	for _, dir := range []string{p.ctx.WorkspaceCwd, p.ctx.FocusedPaneCwd} {
		if fs.IsDir(dir) {
			return dir
		}
	}
	return env.Home()
}

func (p *Panel) load() {
	p.cmds = nil
	p.lineNos = nil
	raw, err := os.ReadFile(p.env.CommandsFile())
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
func (p *Panel) commandsChanged() bool {
	info, err := os.Stat(p.env.CommandsFile())
	if err != nil {
		return false
	}
	marker, err := os.Stat(p.marker)
	if err != nil {
		return true
	}
	return info.ModTime().After(marker.ModTime())
}

func (p *Panel) touchMarker() {
	now := time.Now()
	// Chtimes alone cannot bring back a marker removed from outside, and a
	// missing one reads as a change on every pass.
	if f, err := os.Create(p.marker); err == nil {
		_ = f.Close()
	}
	_ = os.Chtimes(p.marker, now, now)
}

func (p *Panel) draw() {
	p.rows, p.cols = terminal.Size()
	p.cols = max(p.cols, 20)

	exitMarker := ""
	if raw, err := os.ReadFile(p.env.RunExitFile()); err == nil {
		exitMarker = strings.TrimSpace(string(raw))
	}
	if p.runCmd == "" {
		if raw, err := os.ReadFile(p.env.RunCmdFile()); err == nil {
			p.runCmd = strings.TrimSpace(string(raw))
		}
	}

	p.showOut = false
	if !p.outDismissed && fs.NonEmpty(p.env.RunLog()) {
		p.showOut = true
		p.outH = min(max((p.rows-3)/3, 5), p.rows-7)
		if p.outH < 5 {
			p.showOut = false
			p.outH = 0
		}
	} else {
		p.outH = 0
	}

	p.listH = max(p.rows-3-p.outH, 1)

	rows := len(p.cmds) + 1
	p.sel = min(max(p.sel, 0), rows-1)
	p.top = min(p.top, p.sel)
	p.top = max(p.top, p.sel-p.listH+1)
	p.top = min(p.top, rows-p.listH)
	p.top = max(p.top, 0)

	// The add row and the output area above it are pinned, so neither moves as
	// commands are added or removed.
	p.vis = min(max(len(p.cmds)-p.top, 0), p.listH)
	p.addRow = p.rows
	p.listEndRow = p.rows - p.outH - 1
	p.outHdrRow = p.rows - p.outH + 1
	p.outContentH = p.outH - 2

	f := terminal.NewFrame()

	switch {
	case p.notice != "" && p.noticeOK:
		f.Row(" " + ansiDim + truncate(p.notice, p.cols-3) + ansiReset)
	case p.notice != "":
		f.Row(" " + ansiRed + "! " + truncate(p.notice, p.cols-4) + ansiReset)
	case p.runCmd != "" && exitMarker == "":
		f.Row(" " + ansiBold + truncate("running: "+p.runCmd, p.cols-3) + ansiReset)
	default:
		f.Row(" " + ansiBold + shortPath(p.titleDir, p.cols-3) + ansiReset)
	}
	rule := strings.Repeat("─", p.cols)
	f.Row(rule)

	for i := p.top; i < p.top+p.vis; i++ {
		label := truncate(" "+p.cmds[i], p.cols-3)
		row := pad(label, p.cols-1)
		if i == p.hover {
			f.Row(ansiInvert + row + ansiRed + "×" + ansiReset)
		} else {
			f.Row(row + ansiRed + "×" + ansiReset)
		}
	}
	for i := p.vis; i < p.listH; i++ {
		f.Row("")
	}

	if p.showOut {
		f.Row(rule)
		header := pad(truncate("output: "+p.runCmd, p.cols-14), p.cols-10)
		f.Row(" " + ansiBold + header + ansiReset + ansiDim + "copy hide" + ansiReset)
		contentH := p.outContentH
		if exitMarker != "" {
			contentH--
		}
		contentH = max(contentH, 0)
		shown := 0
		if contentH > 0 {
			if lines, err := fs.TailLines(p.env.RunLog(), contentH); err == nil {
				for _, line := range lines {
					f.Row(" " + truncate(line, p.cols-2))
					shown++
				}
			}
		}
		for ; shown < contentH; shown++ {
			f.Row("")
		}
		if exitMarker != "" {
			f.Row(" " + ansiDim + "── exited " + exitMarker + " ──" + ansiReset)
		}
	}

	label := truncate(" + Add Command", p.cols-1)
	row := pad(label, p.cols)
	if p.hover == len(p.cmds) {
		f.Row(ansiInvert + row + ansiReset)
	} else {
		f.Row(row)
	}

	// An idle panel draws the same frame every tick: writing it again is a
	// full repaint on the far terminal for nothing.
	if bytes.Equal(f.Bytes(), p.lastFrame) {
		return
	}
	p.lastFrame = append(p.lastFrame[:0], f.Bytes()...)

	p.term.Write(terminal.SeqHome)
	p.term.Write(string(f.Bytes()))
	// Erase to the end of the screen; writing past the last row would scroll.
	p.term.Write(terminal.SeqEraseDown)
	_ = p.term.Flush()
}

func (p *Panel) handleKey(key string) {
	p.notice = ""
	p.noticeOK = false
	p.dirty = true
	switch key {
	case terminal.KeyEnter:
		if p.sel < len(p.cmds) {
			p.runCommand(p.sel)
		} else {
			p.openAdd()
		}
	case terminal.KeyUp, keyUpRune:
		p.moveSel(-1)
	case terminal.KeyDown, keyDownRune:
		p.moveSel(1)
	case terminal.KeyHome, keyTopRune:
		p.sel = 0
	case terminal.KeyEnd, keyEndRune:
		p.sel = len(p.cmds)
	case keyAddRune, terminal.KeySpace:
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
func (p *Panel) moveSel(delta int) {
	if next := p.sel + delta; next >= 0 && next <= len(p.cmds) {
		p.sel = next
	}
}

// Button codes are raw SGR values: 35 motion, 64 and 65 the wheel, 0 a press.
func (p *Panel) handleMouse(m *terminal.MouseEvent) {
	// A release is not a fresh action: clearing the notice on it would wipe the
	// one the press just raised, and a click arrives as a press and a release.
	if m.Button == 0 && !m.Release {
		p.notice = ""
		p.noticeOK = false
	}
	if m.Button == 35 {
		idx := -1
		if m.Y >= 3 && m.Y <= p.listEndRow {
			idx = p.top + m.Y - 3
			if idx >= p.top+p.vis {
				idx = -1
			}
		} else if m.Y == p.addRow {
			idx = len(p.cmds)
		}
		if idx != p.hover {
			p.hover = idx
			p.dirty = true
		}
		return
	}
	if m.Release {
		return
	}
	switch m.Button {
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
	if p.showOut && m.Y >= p.outHdrRow && m.Y < p.addRow {
		if m.Y == p.outHdrRow {
			switch {
			case m.X >= p.cols-3:
				p.outDismissed = true
				p.dirty = true
			case m.X >= p.cols-8:
				p.copyOutput()
			}
		}
		return
	}
	if m.Y == p.addRow {
		p.openAdd()
		return
	}
	if m.Y >= 3 && m.Y <= p.listEndRow {
		idx := p.top + m.Y - 3
		if idx >= len(p.cmds) {
			return
		}
		p.sel = idx
		p.dirty = true
		if m.X >= p.cols-2 {
			p.delete(idx)
		} else {
			p.runCommand(idx)
		}
	}
}

func (p *Panel) runCommand(idx int) {
	p.term.Flush()
	payload := fmt.Sprintf("%s\n%s\n%d\n", p.execCwd(), p.cmds[idx], time.Now().Unix())
	// The hand-over goes first: a failed write must not throw away the output
	// the panel is still showing.
	if err := fs.WriteAtomic(p.env.PendingFile(), payload); err != nil {
		p.notice = "cannot write " + p.env.PendingFile()
		p.noticeOK = false
		p.dirty = true
		return
	}
	_ = os.Remove(p.env.RunLog())
	_ = os.RemoveAll(p.env.RunExitFile())
	p.outDismissed = false
	p.outHideArmed = false
	p.runCmd = p.cmds[idx]
	p.notice = ""
	p.noticeOK = false
	if err := p.herdr.InvokeRunAction(); err != nil {
		// A payload left behind is run by the next invoke of the action, and
		// the panel must not claim a run it never started.
		_ = os.Remove(p.env.PendingFile())
		p.runCmd = ""
		p.outDismissed = true
		p.notice = "cannot start the run action"
	}
	p.dirty = true
}

func (p *Panel) delete(idx int) {
	want := p.lineNos[idx]
	raw, err := os.ReadFile(p.env.CommandsFile())
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
	if err := fs.WriteAtomic(p.env.CommandsFile(), strings.Join(kept, "\n")); err != nil {
		return
	}
	p.load()
	if p.sel >= len(p.cmds) {
		p.sel = len(p.cmds)
	}
	p.dirty = true
}

func (p *Panel) openAdd() {
	p.notice = ""
	p.noticeOK = false
	if err := p.herdr.OpenPopup("add"); err != nil {
		p.notice = "cannot open the add popup"
	}
	p.dirty = true
}

// recordWidth stores the share of the tab a reopened pane should come back at.
func (p *Panel) recordWidth() {
	if p.env.PaneID == "" {
		return
	}
	paneWidth, tabWidth, err := p.herdr.LayoutWidths(p.env.PaneID)
	if err != nil || tabWidth <= 0 {
		return
	}
	permille := (paneWidth*1000 + tabWidth/2) / tabWidth
	if permille < 30 || permille > 900 {
		return
	}
	_ = fs.WriteAtomic(p.env.WidthFile(), strconv.Itoa(permille)+"\n")
}

// copyOutput uses OSC 52: the panel captures the mouse, so drag-select is
// unavailable.
func (p *Panel) copyOutput() {
	if !fs.NonEmpty(p.env.RunLog()) {
		return
	}
	raw, err := fs.TailBytes(p.env.RunLog(), outputTailBytes)
	if err != nil || len(raw) == 0 {
		return
	}
	p.term.Write("\033]52;c;" + base64.StdEncoding.EncodeToString(raw) + "\a")
	p.notice = "output copied to the clipboard"
	p.noticeOK = true
	p.dirty = true
}
