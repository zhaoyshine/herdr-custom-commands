//go:build linux

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func pluginCLI(args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(pluginRoot, "bin", "hcc"), args...)
	cmd.Dir = pluginRoot
	cmd.Env = []string{"HOME=" + srvHome, "PATH=" + pathEnv, "SHELL=/bin/bash", "HERDR_BIN_PATH=" + herdrBin}
	return cmd
}

type client struct {
	t      *testing.T
	master *os.File
	cmd    *exec.Cmd
	done   chan struct{}
	mu     sync.Mutex
	out    bytes.Buffer
}

func newClient(t *testing.T) *client {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		t.Fatalf("TIOCGPTN: %v", errno)
	}
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Fatalf("TIOCSPTLCK: %v", errno)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open the pty slave: %v", err)
	}
	win := struct{ rows, cols, x, y uint16 }{50, 200, 0, 0}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&win))); errno != 0 {
		t.Fatalf("TIOCSWINSZ: %v", errno)
	}
	cmd := exec.Command(herdrBin)
	cmd.Env = []string{"HOME=" + srvHome, "PATH=" + pathEnv, "SHELL=/bin/bash", "TERM=xterm-256color", "LANG=C.UTF-8"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the Herdr client: %v", err)
	}
	_ = slave.Close()
	c := &client{t: t, master: master, cmd: cmd, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		buf := make([]byte, 65536)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				c.mu.Lock()
				c.out.Write(buf[:n])
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(c.close)
	return c
}

func (c *client) screen() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

func (c *client) waitAttached(timeout time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		res, err := call("pane.layout", nil)
		if err == nil {
			var out struct {
				Layout struct {
					Area struct {
						Width  int `json:"width"`
						Height int `json:"height"`
					} `json:"area"`
				} `json:"layout"`
			}
			if json.Unmarshal(res, &out) == nil && out.Layout.Area.Height >= 40 && out.Layout.Area.Width >= 150 {
				return
			}
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("the Herdr client did not attach within %v", timeout)
		}
		time.Sleep(clockTick)
	}
}

func (c *client) write(s string) {
	c.t.Helper()
	if _, err := c.master.WriteString(s); err != nil {
		c.t.Fatalf("write to the client pty: %v", err)
	}
}

func (c *client) close() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
	}
	_ = c.master.Close()
}

func Test4TypingInThePopupAddsTheCommand(t *testing.T) {
	c := newClient(t)
	h := newHarness(t)
	h.focus()
	c.waitAttached(panelTimeout)
	h.waitPanel(panelTimeout)

	line := "echo " + marker("add")
	cols, addRow := h.geometry()
	h.click(cols/2, addRow)
	time.Sleep(2 * time.Second)
	c.write(line)
	time.Sleep(500 * time.Millisecond)
	c.write("\r")

	if !waitForSoft("the popup to save the command", commandTimeout, func() bool {
		return strings.Contains(h.commands(), line+"\n")
	}) {
		c.t.Fatalf("the popup did not save %q\nclient screen tail:\n%q\ncommands.txt:\n%s", line, tail(c.screen(), 1500), h.commands())
	}
	h.waitText("the added command", line, commandTimeout)
	assertPopupClosed(t)
}

func waitForSoft(desc string, timeout time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if ok() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(clockTick)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func Test5EscapeInThePopupAddsNothing(t *testing.T) {
	c := newClient(t)
	h := newHarness(t)
	h.focus()
	c.waitAttached(panelTimeout)
	h.waitPanel(panelTimeout)

	before := h.commands()
	cols, addRow := h.geometry()
	h.click(cols/2, addRow)
	waitFor(t, "the add popup", 5*time.Second, func() bool { return popupOpen(t) })

	h.click(cols/2, addRow)
	time.Sleep(2 * time.Second)
	c.write("junk")
	time.Sleep(500 * time.Millisecond)
	c.write("\x1b")
	time.Sleep(2 * time.Second)

	if got := h.commands(); got != before {
		t.Fatalf("commands.txt = %q after escape, want %q", got, before)
	}
	assertPopupClosed(t)
}

func Test16TheWidthSurvivesAReopen(t *testing.T) {
	c := newClient(t)
	h := newHarness(t)
	c.waitAttached(panelTimeout)
	h.waitPanel(panelTimeout)
	h.focus()

	if _, err := call("layout.set_split_ratio", map[string]any{"path": []any{}, "ratio": 0.35}); err != nil {
		t.Fatalf("layout.set_split_ratio: %v", err)
	}
	widthFile := filepath.Join(configDir, "width")
	var permille int
	waitFor(t, "the remembered width", 15*time.Second, func() bool {
		raw, err := os.ReadFile(widthFile)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || n < 600 || n > 700 {
			return false
		}
		permille = n
		return true
	})

	area, rect := layoutWidths(t, h.panel)
	if diff := rect - (area*permille+500)/1000; diff < -2 || diff > 2 {
		t.Fatalf("the panel is %d cells of %d, want %d permille (diff %d)", rect, area, permille, diff)
	}

	reopenPanel(t, h)

	area, rect = layoutWidths(t, h.panel)
	want := (area*permille + 500) / 1000
	if diff := rect - want; diff < -2 || diff > 2 {
		t.Fatalf("the reopened panel is %d cells of %d, want %d (remembered %d permille, diff %d)", rect, area, want, permille, diff)
	}
	cols, _ := h.geometry()
	if diff := cols - (rect - 2); diff < -2 || diff > 2 {
		t.Fatalf("the reopened panel renders %d columns, want %d (diff %d)", cols, rect-2, diff)
	}
}

func layoutWidths(t *testing.T, paneID string) (area, rect int) {
	t.Helper()
	var out struct {
		Layout struct {
			Area struct {
				Width int `json:"width"`
			} `json:"area"`
			Panes []struct {
				PaneID string `json:"pane_id"`
				Rect   struct {
					Width int `json:"width"`
				} `json:"rect"`
			} `json:"panes"`
		} `json:"layout"`
	}
	if err := json.Unmarshal(must(t, "pane.layout", map[string]any{"pane_id": paneID}), &out); err != nil {
		t.Fatalf("pane.layout: %v", err)
	}
	for _, p := range out.Layout.Panes {
		if p.PaneID == paneID {
			return out.Layout.Area.Width, p.Rect.Width
		}
	}
	t.Fatalf("pane %s is not in its layout", paneID)
	return 0, 0
}

func reopenPanel(t *testing.T, h *harness) {
	t.Helper()
	must(t, "plugin.pane.close", map[string]any{"pane_id": h.panel})
	waitFor(t, "the panel pane to close", 10*time.Second, func() bool { return len(h.panels()) == 0 })
	if out, err := pluginCLI("startup").CombinedOutput(); err != nil {
		t.Fatalf("hcc startup: %v (%s)", err, out)
	}
	h.waitPanel(panelTimeout)
}
