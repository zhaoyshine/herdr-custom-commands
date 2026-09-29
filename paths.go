package main

import (
	"os"
	"path/filepath"
)

// env is the plugin's runtime environment. Herdr sets it for every command it
// runs, but a pane restored from a session snapshot may carry only the
// environment the snapshot captured, so every lookup has a fallback.
type env struct {
	root    string
	config  string
	state   string
	herdr   string
	paneID  string
	spaceID string
	context string
}

func loadEnv() env {
	config := os.Getenv("HERDR_PLUGIN_CONFIG_DIR")
	state := os.Getenv("HERDR_PLUGIN_STATE_DIR")
	root := os.Getenv("HERDR_PLUGIN_ROOT")

	if root == "" {
		// The executable lives in the plugin's bin/ directory, so the plugin
		// root is its parent's parent.
		if exe, err := os.Executable(); err == nil {
			root = filepath.Dir(filepath.Dir(exe))
		}
	}
	if config == "" {
		config = filepath.Join(xdgConfigHome(), "herdr", "plugins", "config", pluginID)
		state = filepath.Join(xdgStateHome(), "herdr", "plugins", pluginID)
	} else if state == "" {
		state = config
	}

	e := env{
		root:    root,
		config:  config,
		state:   state,
		herdr:   os.Getenv("HERDR_BIN_PATH"),
		paneID:  os.Getenv("HERDR_PANE_ID"),
		spaceID: os.Getenv("HERDR_WORKSPACE_ID"),
		context: os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"),
	}
	if e.herdr == "" {
		e.herdr = "herdr"
	}
	return e
}

func xdgConfigHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(home(), ".config")
}

func xdgStateHome() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(home(), ".local", "state")
}

func home() string {
	if dir := os.Getenv("HOME"); dir != "" {
		return dir
	}
	return "/"
}

func (e env) commandsFile() string { return filepath.Join(e.config, "commands.txt") }
func (e env) widthFile() string    { return filepath.Join(e.config, "width") }
func (e env) timeoutFile() string  { return filepath.Join(e.config, "output-timeout") }
func (e env) pendingFile() string  { return filepath.Join(e.state, "pending") }
func (e env) runLog() string       { return filepath.Join(e.state, "last-run.log") }
func (e env) runExitFile() string  { return filepath.Join(e.state, "last-run.exit") }
func (e env) runCmdFile() string   { return filepath.Join(e.state, "last-run.cmd") }
func (e env) markerFile(suffix string) string {
	return filepath.Join(e.state, ".loaded."+suffix)
}

// No panel reads legacyMarker. Each panel deletes it, so a stale file cannot be
// mistaken for a live marker.
func (e env) legacyMarker() string { return filepath.Join(e.state, ".loaded") }
