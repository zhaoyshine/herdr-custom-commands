package e2e_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	pluginID    = "herdr-custom-commands"
	panelLabel  = "Custom Commands"
	addRowLabel = "+ Add Command"

	clockTick      = 200 * time.Millisecond
	panelTimeout   = 15 * time.Second
	commandTimeout = 10 * time.Second
)

var (
	herdrBin   string
	pathEnv    string
	repoRoot   string
	pluginRoot string
	srvHome    string
	sockPath   string
	configDir  string
	stateDir   string
	server     *exec.Cmd
)

func TestMain(m *testing.M) {
	if bin := os.Getenv("HCC_E2E_HERDR"); bin != "" {
		herdrBin = bin
	} else if bin, err := exec.LookPath("herdr"); err == nil {
		herdrBin = bin
	}
	if herdrBin == "" {
		fmt.Println("e2e: herdr not found in PATH and HCC_E2E_HERDR unset, skipping the suite")
		os.Exit(0)
	}
	pathEnv = filepath.Dir(herdrBin) + ":" + os.Getenv("PATH")

	wd, err := os.Getwd()
	if err != nil {
		fmt.Println("e2e:", err)
		os.Exit(1)
	}
	repoRoot = filepath.Dir(wd)

	root, err := os.MkdirTemp("", "hcc-e2e-")
	if err != nil {
		fmt.Println("e2e:", err)
		os.Exit(1)
	}
	srvHome = filepath.Join(root, "home")
	pluginRoot = filepath.Join(root, "plugin")
	configDir = filepath.Join(srvHome, ".config", "herdr", "plugins", "config", pluginID)
	stateDir = filepath.Join(srvHome, ".local", "state", "herdr", "plugins", pluginID)
	sockPath = filepath.Join(srvHome, ".config", "herdr", "herdr.sock")

	for _, dir := range []string{filepath.Join(pluginRoot, "bin"), configDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Println("e2e:", err)
			os.Exit(1)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(repoRoot, "herdr-plugin.toml"))
	if err != nil {
		fmt.Println("e2e:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "herdr-plugin.toml"), manifest, 0o644); err != nil {
		fmt.Println("e2e:", err)
		os.Exit(1)
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(pluginRoot, "bin", "hcc"), "./cmd/hcc")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Printf("e2e: go build: %v\n%s", err, out)
		os.Exit(1)
	}
	fixtures := map[string]string{
		filepath.Join(configDir, "output-timeout"):                "0\n",
		filepath.Join(srvHome, ".bashrc"):                         "alias e2ealias='echo ALIAS-E2E-OK'\n",
		filepath.Join(srvHome, ".config", "herdr", "config.toml"): "onboarding = false\n\n[update]\nversion_check = false\nmanifest_check = false\n",
	}
	for path, content := range fixtures {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Println("e2e:", err)
			os.Exit(1)
		}
	}

	if err := startServer(); err != nil {
		fmt.Println("e2e:", err)
		os.Exit(1)
	}
	if err := waitSocket(30 * time.Second); err != nil {
		fmt.Println("e2e:", err)
		stopServer()
		os.Exit(1)
	}
	if _, err := call("plugin.link", map[string]any{"path": pluginRoot}); err != nil {
		fmt.Println("e2e: plugin.link:", err)
		stopServer()
		os.Exit(1)
	}

	code := m.Run()
	stopServer()
	os.RemoveAll(root)
	os.Exit(code)
}

func startServer() error {
	logPath := filepath.Join(srvHome, ".config", "herdr", "herdr-server.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(herdrBin, "server")
	cmd.Dir = pluginRoot
	cmd.Env = []string{"HOME=" + srvHome, "PATH=" + pathEnv, "SHELL=/bin/bash", "TERM=xterm-256color"}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	server = cmd
	return nil
}

func stopServer() {
	if server == nil {
		return
	}
	_, _ = call("server.stop", nil)
	done := make(chan struct{})
	go func() {
		_ = server.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = server.Process.Kill()
		<-done
	}
	server = nil
}

func restartServer(t *testing.T) {
	t.Helper()
	stopServer()
	if err := startServer(); err != nil {
		t.Fatalf("restart the Herdr server: %v", err)
	}
	if err := waitSocket(30 * time.Second); err != nil {
		t.Fatalf("restart the Herdr server: %v", err)
	}
	if _, err := call("plugin.link", map[string]any{"path": pluginRoot}); err != nil {
		t.Fatalf("re-link the plugin: %v", err)
	}
}

func waitSocket(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := call("pane.list", nil); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the api socket did not answer within %v", timeout)
		}
		time.Sleep(clockTick)
	}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func call(method string, params map[string]any) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	payload, err := json.Marshal(map[string]any{"id": "e2e", "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *apiError       `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func must(t *testing.T, method string, params map[string]any) json.RawMessage {
	t.Helper()
	res, err := call(method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return res
}

type paneInfo struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Cwd         string `json:"cwd"`
	Focused     bool   `json:"focused"`
}

func listPanes(t *testing.T, workspace string) []paneInfo {
	t.Helper()
	params := map[string]any{}
	if workspace != "" {
		params["workspace_id"] = workspace
	}
	var out struct {
		Panes []paneInfo `json:"panes"`
	}
	if err := json.Unmarshal(must(t, "pane.list", params), &out); err != nil {
		t.Fatalf("pane.list: %v", err)
	}
	return out.Panes
}

type harness struct {
	t     *testing.T
	dir   string
	ws    string
	root  string
	panel string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dir: filepath.Join(srvHome, t.Name())}
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		t.Fatalf("create the workspace directory: %v", err)
	}
	h.resetRunState()
	var out struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if err := json.Unmarshal(must(t, "workspace.create", map[string]any{"cwd": h.dir}), &out); err != nil {
		t.Fatalf("workspace.create: %v", err)
	}
	h.ws, h.root = out.Workspace.WorkspaceID, out.RootPane.PaneID
	t.Cleanup(func() {
		_, _ = call("workspace.close", map[string]any{"workspace_id": h.ws})
	})
	h.waitPanel(panelTimeout)
	return h
}

func (h *harness) resetRunState() {
	for _, name := range []string{"last-run.log", "last-run.exit", "last-run.cmd", "pending"} {
		_ = os.Remove(filepath.Join(stateDir, name))
	}
}

func (h *harness) commandsPath() string { return filepath.Join(configDir, "commands.txt") }

func (h *harness) statePath(name string) string { return filepath.Join(stateDir, name) }

func (h *harness) panels() []paneInfo {
	panes := listPanes(h.t, h.ws)
	var found []paneInfo
	for _, p := range panes {
		if p.Label == panelLabel {
			found = append(found, p)
		}
	}
	return found
}

func (h *harness) waitPanel(timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		panes := h.panels()
		if len(panes) == 1 {
			h.panel = panes[0].PaneID
			if cols, addRow := h.geometry(); cols > 0 && addRow > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no panel rendered in workspace %s within %v (panes: %v)", h.ws, timeout, h.panels())
		}
		time.Sleep(clockTick)
	}
}

func (h *harness) frame() []string {
	res, err := call("pane.read", map[string]any{"pane_id": h.panel, "source": "visible", "format": "text"})
	if err != nil {
		return nil
	}
	var out struct {
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil
	}
	lines := strings.Split(out.Read.Text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

func (h *harness) text() string { return strings.Join(h.frame(), "\n") }

func (h *harness) geometry() (cols, addRow int) {
	for i, line := range h.frame() {
		if cols == 0 && strings.HasPrefix(line, "─") {
			cols = len([]rune(line))
		}
		if strings.TrimSpace(line) == addRowLabel {
			addRow = i + 1
		}
	}
	return cols, addRow
}

func (h *harness) title() string {
	lines := h.frame()
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func (h *harness) send(text string) {
	h.t.Helper()
	if _, err := call("pane.send_text", map[string]any{"pane_id": h.panel, "text": text}); err != nil {
		h.t.Fatalf("pane.send_text: %v", err)
	}
}

func (h *harness) click(x, y int) {
	h.send(fmt.Sprintf("\033[<0;%d;%dM\033[<0;%d;%dm", x, y, x, y))
}

func (h *harness) keys(keys ...string) {
	h.t.Helper()
	if _, err := call("pane.send_keys", map[string]any{"pane_id": h.panel, "keys": keys}); err != nil {
		h.t.Fatalf("pane.send_keys %v: %v", keys, err)
	}
}

func (h *harness) sendRoot(keys ...string) {
	h.t.Helper()
	if _, err := call("pane.send_keys", map[string]any{"pane_id": h.root, "keys": keys}); err != nil {
		h.t.Fatalf("pane.send_keys %v to the root pane: %v", keys, err)
	}
}

func (h *harness) focus() {
	h.t.Helper()
	must(h.t, "workspace.focus", map[string]any{"workspace_id": h.ws})
}

func (h *harness) writeCommands(lines ...string) {
	h.t.Helper()
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(h.commandsPath(), []byte(content), 0o644); err != nil {
		h.t.Fatalf("write commands.txt: %v", err)
	}
}

func (h *harness) commands() string {
	raw, err := os.ReadFile(h.commandsPath())
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		h.t.Fatalf("read commands.txt: %v", err)
	}
	return string(raw)
}

func (h *harness) waitText(desc, want string, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if strings.Contains(h.text(), want) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: %q did not appear within %v\npanel:\n%s", desc, want, timeout, h.text())
		}
		time.Sleep(clockTick)
	}
}

func (h *harness) waitNoText(desc, unwanted string, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if !strings.Contains(h.text(), unwanted) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: %q is still there after %v\npanel:\n%s", desc, unwanted, timeout, h.text())
		}
		time.Sleep(clockTick)
	}
}

func waitFor(t *testing.T, desc string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ok() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", desc)
		}
		time.Sleep(clockTick)
	}
}

func closePopup(t *testing.T) error {
	t.Helper()
	_, err := call("popup.close", nil)
	return err
}

func assertPopupOpen(t *testing.T) {
	t.Helper()
	if err := closePopup(t); err != nil {
		t.Fatalf("popup.close = %v, want a popup to close", err)
	}
}

func assertPopupClosed(t *testing.T) {
	t.Helper()
	var apiErr *apiError
	if err := closePopup(t); !errors.As(err, &apiErr) || apiErr.Code != "popup_not_open" {
		t.Fatalf("popup.close = %v, want popup_not_open", err)
	}
}

func popupOpen(t *testing.T) bool {
	t.Helper()
	return closePopup(t) == nil
}

func (h *harness) runFirstCommand() {
	h.t.Helper()
	cols, _ := h.geometry()
	h.click(cols/2, 3)
}

func (h *harness) outputHeaderRow() (cols, row int) {
	cols, addRow := h.geometry()
	outH := min(max((addRow-3)/3, 5), addRow-7)
	return cols, addRow - outH + 1
}

func marker(tag string) string {
	return fmt.Sprintf("e2e-%s-%d", tag, time.Now().UnixNano()%1000000)
}

func Test1PanelIsTheOnlyPaneOpened(t *testing.T) {
	h := newHarness(t)
	h.writeCommands("echo one-"+marker("list"), "# a comment line", "", "echo two-"+marker("list"))

	var panes []paneInfo
	waitFor(t, "exactly one Custom Commands pane", 5*time.Second, func() bool {
		panes = h.panels()
		return len(panes) == 1
	})
	if panes[0].Cwd != pluginRoot {
		t.Fatalf("the panel runs in %q, want the plugin root %q", panes[0].Cwd, pluginRoot)
	}
	if len(listPanes(t, h.ws)) != 2 {
		t.Fatalf("the workspace has %d panes, want the root pane and the panel", len(listPanes(t, h.ws)))
	}
	h.waitText("the seeded list", h.commandsLine(3), commandTimeout)

	lines := h.frame()
	var list []string
	for _, line := range lines[2:] {
		line = strings.TrimRight(line, " ×")
		if strings.HasPrefix(line, " echo ") {
			list = append(list, strings.TrimSpace(line))
		}
	}
	want := []string{h.commandsLine(0), h.commandsLine(3)}
	if strings.Join(list, "|") != strings.Join(want, "|") {
		t.Fatalf("the panel lists %q, want %q\npanel:\n%s", list, want, h.text())
	}
	if strings.Contains(h.text(), "a comment line") {
		t.Fatalf("the panel shows the comment line\npanel:\n%s", h.text())
	}
	if _, addRow := h.geometry(); addRow == 0 {
		t.Fatalf("the panel has no %q row\npanel:\n%s", addRowLabel, h.text())
	}
}

func (h *harness) commandsLine(index int) string {
	lines := strings.Split(strings.TrimRight(h.commands(), "\n"), "\n")
	return lines[index]
}

func Test2ClickOnTheCrossDeletesTheCommand(t *testing.T) {
	h := newHarness(t)
	first, second := "echo one-"+marker("del"), "echo two-"+marker("del")
	h.writeCommands(first, second)
	h.waitText("the seeded list", first, commandTimeout)

	cols, _ := h.geometry()
	h.click(cols-2, 3)

	waitFor(t, "the file to lose the first command", 5*time.Second, func() bool {
		return h.commands() == second+"\n"
	})
	h.waitNoText("the deleted command", first, commandTimeout)
	h.waitText("the kept command", second, commandTimeout)
}

func Test3ClickOnTheAddRowOpensThePopup(t *testing.T) {
	h := newHarness(t)
	h.focus()
	assertPopupClosed(t)
	cols, addRow := h.geometry()
	h.click(cols/2, addRow)
	waitFor(t, "the add popup", 5*time.Second, func() bool { return popupOpen(t) })
	assertPopupClosed(t)
}

func Test6ClickOnACommandRunsIt(t *testing.T) {
	h := newHarness(t)
	mark := marker("run")
	h.writeCommands("echo " + mark)
	h.waitText("the seeded command", mark, commandTimeout)

	h.runFirstCommand()

	h.waitText("the command output", mark+"\n", commandTimeout)
	waitFor(t, "the exit marker", commandTimeout, func() bool {
		return strings.Contains(h.text(), "── exited 0 ──")
	})
}

func Test7ClickOnCopyShowsTheNotice(t *testing.T) {
	h := newHarness(t)
	mark := marker("copy")
	h.writeCommands("echo " + mark)
	h.waitText("the seeded command", mark, commandTimeout)

	h.runFirstCommand()
	waitFor(t, "the exit marker", commandTimeout, func() bool {
		return strings.Contains(h.text(), "── exited 0 ──")
	})

	cols, headerRow := h.outputHeaderRow()
	h.click(cols-5, headerRow)

	time.Sleep(2 * time.Second)
	lines := h.frame()
	if len(lines) == 0 || !strings.Contains(lines[0], "output copied to the clipboard") {
		t.Fatalf("the status line is %q two seconds after the copy click, want the copy notice", lines[0])
	}
}

func Test8ClickOnHideDismissesTheOutputUntilTheNextRun(t *testing.T) {
	h := newHarness(t)
	mark := marker("hide")
	h.writeCommands("echo " + mark)
	h.waitText("the seeded command", mark, commandTimeout)

	h.runFirstCommand()
	h.waitText("the output header", "output: echo "+mark, commandTimeout)

	cols, headerRow := h.outputHeaderRow()
	h.click(cols-1, headerRow)

	h.waitNoText("the dismissed output", "output: echo "+mark, commandTimeout)
	h.waitNoText("the dismissed exit marker", "── exited 0 ──", commandTimeout)

	h.runFirstCommand()
	h.waitText("the output back", "output: echo "+mark, commandTimeout)
}

func Test9RestartBringsThePanelsBack(t *testing.T) {
	first, second := newHarness(t), newHarness(t)

	otherRoot := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatalf("create the other install root: %v", err)
	}
	var split struct {
		Pane paneInfo `json:"pane"`
	}
	if err := json.Unmarshal(must(t, "pane.split", map[string]any{
		"target_pane_id": second.root,
		"direction":      "down",
		"cwd":            otherRoot,
		"focus":          false,
	}), &split); err != nil {
		t.Fatalf("pane.split: %v", err)
	}
	must(t, "pane.rename", map[string]any{"pane_id": split.Pane.PaneID, "label": panelLabel})

	restartServer(t)

	waitFor(t, "one panel pane per workspace", 25*time.Second, func() bool {
		return len(first.panels()) == 1 && len(second.panels()) == 1
	})
	first.panel = first.panels()[0].PaneID
	second.panel = second.panels()[0].PaneID
	first.waitPanel(25 * time.Second)
	second.waitPanel(25 * time.Second)
}

func Test10ASecondPanelExitsAtOnce(t *testing.T) {
	h := newHarness(t)
	mark := marker("dup")
	h.writeCommands("echo " + mark)
	h.waitText("the seeded command", mark, commandTimeout)

	var opened struct {
		PluginPane struct {
			Pane paneInfo `json:"pane"`
		} `json:"plugin_pane"`
	}
	if err := json.Unmarshal(must(t, "plugin.pane.open", map[string]any{
		"plugin_id":      pluginID,
		"entrypoint":     "panel",
		"placement":      "split",
		"direction":      "right",
		"target_pane_id": h.root,
		"focus":          false,
	}), &opened); err != nil {
		t.Fatalf("plugin.pane.open: %v", err)
	}
	if opened.PluginPane.Pane.PaneID == "" || opened.PluginPane.Pane.PaneID == h.panel {
		t.Fatalf("the second panel opened as %q, want a new pane next to %q", opened.PluginPane.Pane.PaneID, h.panel)
	}

	waitFor(t, "the duplicate pane to exit", 15*time.Second, func() bool {
		for _, p := range listPanes(t, h.ws) {
			if p.PaneID == opened.PluginPane.Pane.PaneID {
				return false
			}
		}
		return len(h.panels()) == 1
	})
	if cols, addRow := h.geometry(); cols == 0 || addRow == 0 {
		t.Fatalf("the surviving panel stopped drawing\npanel:\n%s", h.text())
	}
}

func Test11TitleFollowsTheFocusedPaneCd(t *testing.T) {
	h := newHarness(t)
	h.focus()
	must(t, "pane.focus", map[string]any{"pane_id": h.root})
	sub := filepath.Join(h.dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("create the subdirectory: %v", err)
	}
	if _, err := call("pane.send_text", map[string]any{"pane_id": h.root, "text": "cd " + sub}); err != nil {
		t.Fatalf("pane.send_text: %v", err)
	}
	h.sendRoot("enter")

	want := "~/" + filepath.Join(filepath.Base(h.dir), "sub")
	waitFor(t, "the panel title to follow the cd", 20*time.Second, func() bool {
		return h.title() == want
	})
}

func Test12RunUsesThePaneDirectoryAndTheRcAlias(t *testing.T) {
	h := newHarness(t)
	h.writeCommands("e2ealias && pwd")
	h.waitText("the seeded command", "e2ealias", commandTimeout)

	h.runFirstCommand()

	h.waitText("the alias output", "ALIAS-E2E-OK", commandTimeout)
	waitFor(t, "the run directory", commandTimeout, func() bool {
		return strings.Contains(h.text(), h.dir)
	})
	waitFor(t, "the exit marker", commandTimeout, func() bool {
		return strings.Contains(h.text(), "── exited 0 ──")
	})
}

func Test13KeyboardRunsAndDeletes(t *testing.T) {
	h := newHarness(t)
	first, second := "echo one-"+marker("kb"), "echo two-"+marker("kb")
	h.writeCommands(first, second)
	h.waitText("the seeded list", second, commandTimeout)

	h.keys("j")
	time.Sleep(300 * time.Millisecond)
	h.keys("enter")
	h.waitText("the second command's output", "output: "+second, commandTimeout)
	h.waitText("the exit marker", "── exited 0 ──", commandTimeout)

	h.keys("a")
	waitFor(t, "the add popup", 5*time.Second, func() bool { return popupOpen(t) })

	h.keys("g")
	time.Sleep(300 * time.Millisecond)
	h.keys("d")
	waitFor(t, "the file to lose the first command", 5*time.Second, func() bool {
		return h.commands() == second+"\n"
	})
	h.waitNoText("the deleted command", first, commandTimeout)
}

func Test14OutputHidesItselfAfterTheTimeout(t *testing.T) {
	if err := os.WriteFile(filepath.Join(configDir, "output-timeout"), []byte("2\n"), 0o644); err != nil {
		t.Fatalf("write output-timeout: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(configDir, "output-timeout"), []byte("0\n"), 0o644)
	})

	h := newHarness(t)
	mark := marker("timeout")
	h.writeCommands("echo " + mark)
	h.waitText("the seeded command", mark, commandTimeout)

	h.runFirstCommand()
	h.waitText("the exit marker", "── exited 0 ──", commandTimeout)
	h.waitText("the output", "output: echo "+mark, commandTimeout)

	waitFor(t, "the output to hide itself", 20*time.Second, func() bool {
		return !strings.Contains(h.text(), "output: echo "+mark)
	})
}

func Test15TheRunSurvivesItsPane(t *testing.T) {
	h := newHarness(t)
	mark := marker("detach")
	command := "sleep 3 && echo " + mark
	h.writeCommands(command)
	h.waitText("the seeded command", mark, commandTimeout)

	h.runFirstCommand()
	waitFor(t, "the run to start", commandTimeout, func() bool {
		raw, err := os.ReadFile(h.statePath("last-run.cmd"))
		return err == nil && strings.TrimSpace(string(raw)) == command
	})

	must(t, "plugin.pane.close", map[string]any{"pane_id": h.panel})
	waitFor(t, "the panel pane to go", 10*time.Second, func() bool { return len(h.panels()) == 0 })

	waitFor(t, "the detached run to finish", 15*time.Second, func() bool {
		log, err := os.ReadFile(h.statePath("last-run.log"))
		if err != nil || !strings.Contains(string(log), mark) {
			return false
		}
		exit, err := os.ReadFile(h.statePath("last-run.exit"))
		return err == nil && strings.TrimSpace(string(exit)) == "0"
	})
}
