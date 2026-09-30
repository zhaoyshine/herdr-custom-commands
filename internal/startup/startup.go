package startup

import (
	"os"
	"path/filepath"

	"github.com/zhaoyshine/herdr-custom-commands/internal/env"
	"github.com/zhaoyshine/herdr-custom-commands/internal/herdr"
	"github.com/zhaoyshine/herdr-custom-commands/internal/panel"
)

// A workspace as this run found it: where a new panel would go, the pane a
// restored session left in the panel's place, and whether a panel is already
// running there.
type workspace struct {
	target string
	stale  []string
	live   bool
}

func Run(e env.Env) int {
	c := herdr.New(e.Herdr, env.PluginID)
	panes, err := c.Panes("")
	if err != nil || len(panes) == 0 {
		return 0
	}

	// The label is the plugin's, whichever directory the install it came from
	// sits in; only the process says whether a panel is still running there.
	found := make(map[string]*workspace)
	for _, p := range panes {
		if p.WorkspaceID == "" || p.PaneID == "" {
			continue
		}
		w := found[p.WorkspaceID]
		if w == nil {
			w = &workspace{}
			found[p.WorkspaceID] = w
		}
		if p.Label != panel.Label {
			if w.target == "" {
				w.target = p.PaneID
			}
			continue
		}
		// A pane a restart brought back as a shell keeps the label but holds no
		// panel: counting it would leave the workspace without one for good.
		if runsPanel(c, p.PaneID) {
			w.live = true
			continue
		}
		w.stale = append(w.stale, p.PaneID)
	}

	permille := panel.WidthPermille(e)
	for _, w := range found {
		if w.live || w.target == "" {
			continue
		}
		// Every one of them goes: the panel refuses to run beside a pane
		// wearing its label.
		for _, paneID := range w.stale {
			_ = c.ClosePane(paneID)
		}
		paneID, err := c.OpenPanel(w.target)
		if err != nil {
			continue
		}
		panel.ApplyWidth(c, paneID, permille)
	}
	return 0
}

// runsPanel reads a pane's foreground processes. A pane it cannot read is left
// alone rather than replaced.
func runsPanel(c *herdr.Client, paneID string) bool {
	procs, err := c.Processes(paneID)
	if err != nil {
		return true
	}
	// The manifest runs bin/hcc, a launcher that re-execs bin/hcc-<os>-<arch>,
	// so the panel process carries this binary's name, not the manifest's.
	want, err := os.Executable()
	if err != nil {
		return true
	}
	name := filepath.Base(want)
	for _, p := range procs {
		if len(p.Argv) >= 2 && filepath.Base(p.Argv[0]) == name && p.Argv[1] == "panel" {
			return true
		}
	}
	return false
}
