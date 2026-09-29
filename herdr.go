package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// pane is one entry of `pane list`.
type pane struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	Focused     bool   `json:"focused"`
	Cwd         string `json:"cwd"`
	Label       string `json:"label"`
}

type invocationContext struct {
	WorkspaceCwd   string `json:"workspace_cwd"`
	FocusedPaneCwd string `json:"focused_pane_cwd"`
}

func parseContext(raw string) invocationContext {
	var ctx invocationContext
	if raw == "" {
		return ctx
	}
	if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
		return invocationContext{}
	}
	return ctx
}

func (e env) panes(workspaceID string) ([]pane, error) {
	args := []string{"pane", "list"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	var out struct {
		Result struct {
			Panes []pane `json:"panes"`
		} `json:"result"`
	}
	if err := e.decode(args, &out); err != nil {
		return nil, err
	}
	return out.Result.Panes, nil
}

func (e env) layoutWidths(paneID string) (paneWidth, tabWidth int, err error) {
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
	if err = e.decode([]string{"pane", "layout", "--pane", paneID}, &out); err != nil {
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

func (e env) resizePane(paneID, direction string, amount float64) error {
	_, err := e.run("pane", "resize", "--pane", paneID,
		"--direction", direction, "--amount", strconv.FormatFloat(amount, 'f', 3, 64))
	return err
}

// A split cannot be placed by workspace id, so the call targets a pane.
func (e env) openPanel(targetPane string) (string, error) {
	var out struct {
		Result struct {
			PluginPane struct {
				Pane pane `json:"pane"`
			} `json:"plugin_pane"`
		} `json:"result"`
	}
	err := e.decode([]string{"plugin", "pane", "open",
		"--plugin", pluginID,
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

func (e env) openPopup(entrypoint string) error {
	_, err := e.run("plugin", "pane", "open",
		"--plugin", pluginID,
		"--entrypoint", entrypoint,
		"--placement", "popup")
	return err
}

func (e env) invokeRunAction() error {
	_, err := e.run("plugin", "action", "invoke", pluginID+".run")
	return err
}

func (e env) run(args ...string) (string, error) {
	cmd := exec.Command(e.herdr, args...)
	cmd.Stdin = nil
	out, err := cmd.Output()
	return string(out), err
}

func (e env) decode(args []string, into any) error {
	out, err := e.run(args...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return errors.New("Herdr returned nothing")
	}
	return json.Unmarshal([]byte(out), into)
}
