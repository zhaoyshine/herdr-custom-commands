package main

func runStartup(e env) int {
	panes, err := e.panes("")
	if err != nil || len(panes) == 0 {
		return 0
	}

	// Plugin panes run with the plugin directory as their working directory,
	// which tells ours apart from another plugin's pane using the same label.
	have := make(map[string]bool)
	for _, p := range panes {
		if p.Label == panelLabel && sameFile(p.Cwd, e.root) {
			have[p.WorkspaceID] = true
		}
	}

	permille := widthPermille(e)
	seen := make(map[string]bool)
	for _, p := range panes {
		if p.WorkspaceID == "" || p.PaneID == "" || seen[p.WorkspaceID] || have[p.WorkspaceID] {
			continue
		}
		seen[p.WorkspaceID] = true
		paneID, err := e.openPanel(p.PaneID)
		if err != nil {
			continue
		}
		applyWidth(e, paneID, permille)
	}
	return 0
}
