// Package herdr talks to the Herdr CLI.
package herdr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Client struct {
	bin    string
	plugin string
}

func New(bin, plugin string) *Client {
	return &Client{bin: bin, plugin: plugin}
}

// Pane is one entry of `pane list`.
type Pane struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	Focused     bool   `json:"focused"`
	Cwd         string `json:"cwd"`
	Label       string `json:"label"`
}

// Context is the invocation context Herdr hands the plugin.
type Context struct {
	WorkspaceCwd   string `json:"workspace_cwd"`
	FocusedPaneCwd string `json:"focused_pane_cwd"`
}

func ParseContext(raw string) Context {
	var ctx Context
	if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
		return Context{}
	}
	return ctx
}

func (c *Client) Panes(workspaceID string) ([]Pane, error) {
	args := []string{"pane", "list"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	var out struct {
		Result struct {
			Panes []Pane `json:"panes"`
		} `json:"result"`
	}
	if err := c.decode(args, &out); err != nil {
		return nil, err
	}
	return out.Result.Panes, nil
}

func (c *Client) LayoutWidths(paneID string) (paneWidth, tabWidth int, err error) {
	var out struct {
		Result struct {
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
		} `json:"result"`
	}
	if err = c.decode([]string{"pane", "layout", "--pane", paneID}, &out); err != nil {
		return 0, 0, err
	}
	tabWidth = out.Result.Layout.Area.Width
	if tabWidth <= 0 {
		return 0, 0, errors.New("tab has no width")
	}
	for _, p := range out.Result.Layout.Panes {
		if p.PaneID == paneID {
			return p.Rect.Width, tabWidth, nil
		}
	}
	return 0, 0, fmt.Errorf("pane %s is not in its layout", paneID)
}

// Process is one foreground process of a pane, as `pane process-info` reports
// it.
type Process struct {
	Argv []string `json:"argv"`
}

func (c *Client) Processes(paneID string) ([]Process, error) {
	var out struct {
		Result struct {
			ProcessInfo struct {
				ForegroundProcesses []Process `json:"foreground_processes"`
			} `json:"process_info"`
		} `json:"result"`
	}
	if err := c.decode([]string{"pane", "process-info", "--pane", paneID}, &out); err != nil {
		return nil, err
	}
	return out.Result.ProcessInfo.ForegroundProcesses, nil
}

func (c *Client) ClosePane(paneID string) error {
	_, err := c.run("pane", "close", paneID)
	return err
}

func (c *Client) ResizePane(paneID, direction string, amount float64) error {
	_, err := c.run("pane", "resize", "--pane", paneID,
		"--direction", direction, "--amount", strconv.FormatFloat(amount, 'f', 3, 64))
	return err
}

// A split cannot be placed by workspace id, so the call targets a pane.
func (c *Client) OpenPanel(targetPane string) (string, error) {
	var out struct {
		Result struct {
			PluginPane struct {
				Pane Pane `json:"pane"`
			} `json:"plugin_pane"`
		} `json:"result"`
	}
	err := c.decode([]string{"plugin", "pane", "open",
		"--plugin", c.plugin,
		"--entrypoint", "panel",
		"--placement", "split",
		"--direction", "right",
		"--target-pane", targetPane,
		"--no-focus"}, &out)
	if err != nil {
		return "", err
	}
	if out.Result.PluginPane.Pane.PaneID == "" {
		return "", errors.New("pane open returned no pane id")
	}
	return out.Result.PluginPane.Pane.PaneID, nil
}

func (c *Client) OpenPopup(entrypoint string) error {
	_, err := c.run("plugin", "pane", "open",
		"--plugin", c.plugin,
		"--entrypoint", entrypoint,
		"--placement", "popup")
	return err
}

func (c *Client) InvokeRunAction() error {
	_, err := c.run("plugin", "action", "invoke", c.plugin+".run")
	return err
}

func (c *Client) run(args ...string) (string, error) {
	cmd := exec.Command(c.bin, args...)
	cmd.Stdin = nil
	out, err := cmd.Output()
	return string(out), err
}

func (c *Client) decode(args []string, into any) error {
	out, err := c.run(args...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return errors.New("Herdr returned nothing")
	}
	return json.Unmarshal([]byte(out), into)
}
